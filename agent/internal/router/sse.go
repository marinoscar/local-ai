package router

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"math"
	"strings"
)

// Router SSE event names (llama.cpp server_models::notify_sse). The README documents model_status;
// the server sends status_change for every status change after the first one, so both carry
// data.status.
const (
	EventModelStatus    = "model_status"
	EventStatusChange   = "status_change"
	EventModelMemory    = "model_memory"
	EventModelRemove    = "model_remove"
	EventModelsReload   = "models_reload"
	EventDownloadProg   = "download_progress"
	EventDownloadDone   = "download_finished"
	EventDownloadFailed = "download_failed"
)

// maxSSELine bounds one line of the stream. A model_status event on first load carries the child's
// /v1/models info, a few KiB; a line over the cap ends the stream (the watcher reconnects).
const maxSSELine = 1 << 20

// Progress is one parsed event of GET /models/sse. It is for the UI and for nudging the watcher to
// poll; correctness never depends on it (llama-router.md section 3.6).
type Progress struct {
	// Model is the model id; "*" for models_reload.
	Model string
	// Event is the router's event name (the Event* constants).
	Event string
	// Status is data.status when the event carries one (loading, loaded, sleeping, unloaded, ...).
	Status Status
	// Stages, Current and Value are data.progress of a loading model: the ordered load stages, the
	// stage running now and the fraction done, 0 to 1. HasValue is false when no progress was sent.
	Stages   []string
	Current  string
	Value    float64
	HasValue bool
	// ExitCode is data.exit_code, sent with status unloaded.
	ExitCode *int
}

// rawEvent is the JSON of one data: line.
type rawEvent struct {
	Model string `json:"model"`
	Event string `json:"event"`
	Data  *struct {
		Status   string `json:"status"`
		ExitCode *int   `json:"exit_code"`
		Progress *struct {
			Stages  []string `json:"stages"`
			Current string   `json:"current"`
			Value   *float64 `json:"value"`
		} `json:"progress"`
	} `json:"data"`
}

// parseEvent decodes the JSON payload of one SSE frame. fallbackName is the frame's event: line,
// used only when the payload has no event field. An event without a model or a name is refused.
func parseEvent(payload []byte, fallbackName string) (Progress, error) {
	var r rawEvent
	// Data may be an object whose progress is a download map (download_progress); decode it
	// leniently by retrying without the typed data when the typed decode fails.
	if err := json.Unmarshal(payload, &r); err != nil {
		var loose struct {
			Model string `json:"model"`
			Event string `json:"event"`
		}
		if err2 := json.Unmarshal(payload, &loose); err2 != nil {
			return Progress{}, malformed("sse event: %v", err2)
		}
		r.Model, r.Event, r.Data = loose.Model, loose.Event, nil
	}
	if r.Event == "" {
		r.Event = fallbackName
	}
	if r.Event == "" || r.Model == "" {
		return Progress{}, malformed("sse event without model or event name")
	}
	p := Progress{Model: r.Model, Event: r.Event}
	if r.Data != nil {
		p.Status = Status(r.Data.Status)
		p.ExitCode = r.Data.ExitCode
		if pr := r.Data.Progress; pr != nil {
			p.Stages, p.Current = pr.Stages, pr.Current
			if pr.Value != nil && !math.IsNaN(*pr.Value) && !math.IsInf(*pr.Value, 0) {
				p.Value = math.Min(1, math.Max(0, *pr.Value))
				p.HasValue = true
			}
		}
	}
	return p, nil
}

// readEvents parses an SSE stream and calls emit for every event until the stream ends, emit
// returns false, or a line exceeds maxSSELine (ErrMalformed). A frame whose payload is not usable
// is skipped (skipped counts them), not fatal: one bad event must not cost the stream. Comment
// lines (": ping") and unknown fields are ignored; both \n and \r\n line ends are accepted.
func readEvents(r io.Reader, emit func(Progress) bool) (skipped int, err error) {
	br := bufio.NewReaderSize(r, 16<<10)
	var data []byte
	var name string
	flush := func() bool {
		defer func() { data, name = data[:0], "" }()
		if len(data) == 0 {
			return true
		}
		p, perr := parseEvent(data, name)
		if perr != nil {
			skipped++
			return true
		}
		return emit(p)
	}
	for {
		line, lerr := readLine(br)
		if lerr != nil {
			if lerr == io.EOF {
				// A frame is complete only at its blank line; a half frame at EOF is dropped.
				return skipped, nil
			}
			return skipped, lerr
		}
		if line == "" {
			if !flush() {
				return skipped, nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "data":
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, value...)
		case "event":
			name = value
		}
	}
}

// readLine reads one line without its terminator, bounded by maxSSELine.
func readLine(br *bufio.Reader) (string, error) {
	var buf bytes.Buffer
	for {
		chunk, isPrefix, err := br.ReadLine()
		if err != nil {
			if err == io.EOF && buf.Len() > 0 {
				return buf.String(), nil
			}
			return "", err
		}
		if buf.Len()+len(chunk) > maxSSELine {
			return "", malformed("sse line over %d bytes", maxSSELine)
		}
		buf.Write(chunk)
		if !isPrefix {
			return buf.String(), nil
		}
	}
}

package hardware

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// OOM sources.
const (
	OOMSourceJournal = "journal"
	OOMSourceService = "service"
)

// llamaProcess is the process name the kernel prints for the router ("(llama-server)").
const llamaProcess = "llama-server"

// OOMEvent is one OOM kill of llama-server (agent.md section 4.4.8).
type OOMEvent struct {
	At      time.Time
	PID     int
	Process string
	// Source is OOMSourceJournal (a kernel journal line) or OOMSourceService (Result=oom-kill).
	Source string
}

var oomLineRE = regexp.MustCompile(`[Oo]ut of memory: Killed process ([0-9]+) \(([^)]*)\)`)

// oomDetector reads the kernel journal for OOM kills of llama-server and takes the service
// watcher's verdict. It remembers what it has seen, so the overlap between two --since windows
// never reports a kill twice.
type oomDetector struct {
	e *env

	mu    sync.Mutex
	since time.Time // end of the last successful journal check
	seen  map[string]bool
	last  *OOMEvent
}

func newOOMDetector(e *env) *oomDetector { return &oomDetector{e: e, seen: map[string]bool{}} }

// check runs one journal query covering the time since the previous check (the first check covers
// the last minute only: kills before the agent started are not news). It returns the new events,
// oldest first. A missing journalctl or a failing query is an error and leaves the window open, so
// the next check covers the gap.
func (d *oomDetector) check(ctx context.Context, now time.Time) ([]OOMEvent, error) {
	d.mu.Lock()
	since := d.since
	d.mu.Unlock()
	if since.IsZero() {
		since = now.Add(-time.Minute)
	}
	out, err := d.e.execOK(ctx, "journalctl", "-k", "-n", "200", "-o", "json",
		"--since="+since.UTC().Format("2006-01-02 15:04:05")+" UTC")
	if err != nil {
		return nil, err
	}
	events := parseJournalOOM(out)

	d.mu.Lock()
	defer d.mu.Unlock()
	d.since = now
	var fresh []OOMEvent
	for _, ev := range events {
		if ev.At.IsZero() {
			ev.At = now // journalctl always stamps records; this is only a guard
		}
		key := fmt.Sprintf("%d@%d", ev.PID, ev.At.UnixMicro())
		if d.seen[key] {
			continue
		}
		d.seen[key] = true
		fresh = append(fresh, ev)
		d.remember(ev)
	}
	if len(d.seen) > 1000 {
		d.seen = map[string]bool{} // far more than a 200-line window can repeat
	}
	return fresh, nil
}

// observeService records the service watcher's verdict: Result=oom-kill of localai-llama.
// Any other result is ignored.
func (d *oomDetector) observeService(result string, at time.Time) (OOMEvent, bool) {
	if result != "oom-kill" {
		return OOMEvent{}, false
	}
	ev := OOMEvent{At: at, Process: llamaProcess, Source: OOMSourceService}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.remember(ev)
	return ev, true
}

func (d *oomDetector) remember(ev OOMEvent) {
	if d.last == nil || ev.At.After(d.last.At) {
		c := ev
		d.last = &c
	}
}

// lastEvent returns the most recent kill seen, if any.
func (d *oomDetector) lastEvent() (OOMEvent, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.last == nil {
		return OOMEvent{}, false
	}
	return *d.last, true
}

// journalLine is the part of a journalctl JSON record that matters.
type journalLine struct {
	Message json.RawMessage `json:"MESSAGE"`
	RealTS  string          `json:"__REALTIME_TIMESTAMP"`
}

// parseJournalOOM extracts the llama-server OOM kills from `journalctl -o json` output (one JSON
// object per line). Lines that are not JSON (a truncated last line), have a binary MESSAGE (an
// array of bytes) or name another process are skipped. The result is ordered as read.
func parseJournalOOM(b []byte) []OOMEvent {
	var out []OOMEvent
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var jl journalLine
		if err := json.Unmarshal(sc.Bytes(), &jl); err != nil {
			continue
		}
		var msg string
		if err := json.Unmarshal(jl.Message, &msg); err != nil {
			continue
		}
		m := oomLineRE.FindStringSubmatch(msg)
		if m == nil || m[2] != llamaProcess {
			continue
		}
		pid, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		var at time.Time
		if us, err := strconv.ParseInt(jl.RealTS, 10, 64); err == nil {
			at = time.UnixMicro(us).UTC()
		}
		out = append(out, OOMEvent{At: at, PID: pid, Process: m[2], Source: OOMSourceJournal})
	}
	return out
}

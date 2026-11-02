package events

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func frames(t *testing.T, in string) ([]Frame, error) {
	t.Helper()
	p := NewParser(strings.NewReader(in))
	var out []Frame
	for {
		f, err := p.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return out, err
		}
		out = append(out, f)
	}
}

func TestParserTable(t *testing.T) {
	long := strings.Repeat("x", MaxFrameBytes+10)
	tests := []struct {
		name string
		in   string
		want []Frame
		err  error
	}{
		{"one event", "id: 7\nevent: model.loaded\ndata: {\"a\":1}\n\n",
			[]Frame{{ID: "7", HasID: true, Event: "model.loaded", Data: `{"a":1}`}}, nil},
		{"multi-line data is joined with a newline", "event: x\ndata: one\ndata: two\ndata:\ndata: four\n\n",
			[]Frame{{Event: "x", Data: "one\ntwo\n\nfour"}}, nil},
		{"missing id leaves HasID false", "event: resync\ndata: {\"reason\":\"gap\"}\n\n",
			[]Frame{{Event: "resync", Data: `{"reason":"gap"}`}}, nil},
		{"ping comment", ": ping\n\n",
			[]Frame{{Comment: true, Text: "ping"}}, nil},
		{"comment between events keeps both", "data: a\n\n: ping\n\ndata: b\n\n",
			[]Frame{{Data: "a"}, {Comment: true, Text: "ping"}, {Data: "b"}}, nil},
		{"comment inside an open frame", "event: x\n: note\ndata: a\n\n",
			[]Frame{{Comment: true, Text: "note"}, {Event: "x", Data: "a"}}, nil},
		{"CRLF line ends", "id: 1\r\nevent: x\r\ndata: a\r\n\r\n",
			[]Frame{{ID: "1", HasID: true, Event: "x", Data: "a"}}, nil},
		{"lone CR line ends", "event: x\rdata: a\r\r",
			[]Frame{{Event: "x", Data: "a"}}, nil},
		{"one leading space only is stripped", "data:  two spaces\n\n",
			[]Frame{{Data: " two spaces"}}, nil},
		{"no space after the colon", "data:tight\n\n",
			[]Frame{{Data: "tight"}}, nil},
		{"field without a colon has an empty value", "data\n\n",
			[]Frame{{Data: ""}}, nil},
		{"unknown fields and retry are ignored", "retry: 3000\nfoo: bar\nevent: x\ndata: a\n\n",
			[]Frame{{Event: "x", Data: "a"}}, nil},
		{"id with a NUL is ignored", "id: 1\x002\ndata: a\n\n",
			[]Frame{{Data: "a"}}, nil},
		{"BOM at the start", "\xef\xbb\xbfdata: a\n\n",
			[]Frame{{Data: "a"}}, nil},
		{"event without data is not dispatched", "id: 5\nevent: x\n\ndata: a\n\n",
			[]Frame{{Data: "a"}}, nil},
		{"blank lines alone dispatch nothing", "\n\n\n", nil, nil},
		{"frame open at EOF is discarded", "data: a\n\ndata: b",
			[]Frame{{Data: "a"}}, nil},
		{"partial line at EOF is discarded", "data: a\n\ndata: b\ndata: c",
			[]Frame{{Data: "a"}}, nil},
		{"a line over the limit fails", "data: " + long + "\n\n", nil, ErrFrameTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := frames(t, tt.in)
			if !errors.Is(err, tt.err) {
				t.Fatalf("err = %v, want %v", err, tt.err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("frames = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("frame %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

type readerErr struct {
	data string
	err  error
	done bool
}

func (r *readerErr) Read(p []byte) (int, error) {
	if r.done {
		return 0, r.err
	}
	r.done = true
	return copy(p, r.data), nil
}

func TestParserPassesReaderError(t *testing.T) {
	boom := errors.New("connection reset")
	p := NewParser(&readerErr{data: "data: a\n\ndata: b\n", err: boom})
	if f, err := p.Next(); err != nil || f.Data != "a" {
		t.Fatalf("first = %+v, %v", f, err)
	}
	if _, err := p.Next(); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the reader's", err)
	}
}

func TestStreamDecodesFrames(t *testing.T) {
	in := "id: 1042\nevent: model.loaded\n" +
		`data: {"id":1042,"type":"model.loaded","at":"2026-11-02T15:04:05Z","node":"ai-n3","op_id":"op_7k2m9q4r8t1c","data":{"model":"qwen3.5-9b-q4km","load_ms":7100},"future_field":1}` + "\n\n" +
		"event: resync\ndata: {\"reason\":\"gap\"}\n\n" +
		": ping\n\n" +
		"event: shutdown\ndata: {}\n\n" +
		"id: 9\nevent: some.future_type\ndata: {\"id\":9,\"type\":\"some.future_type\",\"data\":{\"x\":1}}\n\n" +
		"id: 10\nevent: broken\ndata: not json at all\n\n"
	s := NewStream(io.NopCloser(strings.NewReader(in)))
	defer s.Close()

	it, err := s.Next()
	if err != nil {
		t.Fatal(err)
	}
	ev := it.Event
	if !it.HasID || it.ID != "1042" || ev.Type != "model.loaded" || ev.ID != 1042 || ev.Node != "ai-n3" || ev.OpID != "op_7k2m9q4r8t1c" {
		t.Fatalf("event = %+v (%+v)", ev, it)
	}
	if !strings.Contains(string(ev.Data), `"load_ms":7100`) {
		t.Fatalf("data = %s", ev.Data)
	}

	it, _ = s.Next()
	if it.HasID || it.Event.Type != TypeResync || string(it.Event.Data) != `{"reason":"gap"}` {
		t.Fatalf("resync = %+v", it)
	}
	it, _ = s.Next()
	if !it.Ping {
		t.Fatalf("ping = %+v", it)
	}
	it, _ = s.Next()
	if it.HasID || it.Event.Type != TypeShutdown {
		t.Fatalf("shutdown = %+v", it)
	}
	it, _ = s.Next()
	if it.Event.Type != "some.future_type" || it.ID != "9" {
		t.Fatalf("unknown type must pass through: %+v", it)
	}
	it, err = s.Next()
	if err != nil || it.Event.Type != "broken" || len(it.Event.Data) != 0 || it.ID != "10" {
		t.Fatalf("non-JSON data must not fail the stream: %+v, %v", it, err)
	}
	if _, err := s.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want EOF", err)
	}
}

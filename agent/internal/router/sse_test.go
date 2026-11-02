package router

import (
	"errors"
	"strings"
	"testing"
)

func collect(t *testing.T, stream string) ([]Progress, int, error) {
	t.Helper()
	var got []Progress
	skipped, err := readEvents(strings.NewReader(stream), func(p Progress) bool {
		got = append(got, p)
		return true
	})
	return got, skipped, err
}

func TestReadEventsFixture(t *testing.T) {
	got, skipped, err := collect(t, string(fixture(t, "sse_loading.txt")))
	if err != nil || skipped != 0 {
		t.Fatalf("err=%v skipped=%d", err, skipped)
	}
	if len(got) != 7 {
		t.Fatalf("got %d events, want 7: %+v", len(got), got)
	}
	if e := got[0]; e.Model != "model-a-q4km" || e.Event != EventModelStatus || e.Status != StatusLoading || e.HasValue {
		t.Errorf("first = %+v", e)
	}
	p := got[1]
	if p.Event != EventStatusChange || !p.HasValue || p.Value != 0.5 || p.Current != "text_model" || len(p.Stages) != 2 {
		t.Errorf("progress event = %+v", p)
	}
	if d := got[2]; d.Event != EventDownloadProg || d.Status != "" || d.HasValue {
		t.Errorf("download progress = %+v", d)
	}
	if l := got[3]; l.Status != StatusLoaded {
		t.Errorf("loaded = %+v", l)
	}
	if u := got[4]; u.Status != StatusUnloaded || u.ExitCode == nil || *u.ExitCode != 1 {
		t.Errorf("unloaded = %+v", u)
	}
	if got[5].Event != EventModelRemove || got[6].Model != "*" || got[6].Event != EventModelsReload {
		t.Errorf("tail = %+v %+v", got[5], got[6])
	}
}

func TestReadEventsFraming(t *testing.T) {
	frame := `{"model":"m","event":"model_status","data":{"status":"loaded"}}`
	cases := []struct {
		name    string
		stream  string
		want    int
		skipped int
	}{
		{"crlf", "data: " + frame + "\r\n\r\n", 1, 0},
		{"no space after colon", "data:" + frame + "\n\n", 1, 0},
		{"data split over two lines", "data: {\"model\":\"m\",\ndata: \"event\":\"model_status\"}\n\n", 1, 0},
		{"event line as fallback name", "event: model_status\ndata: {\"model\":\"m\"}\n\n", 1, 0},
		{"ids and retry ignored", "id: 4\nretry: 100\ndata: " + frame + "\n\n", 1, 0},
		{"comments only", ": ping\n\n: ping\n\n", 0, 0},
		{"half frame at EOF dropped", "data: " + frame, 0, 0},
		{"final line without newline then blank", "data: " + frame + "\n\n", 1, 0},
		{"bad json skipped, next one kept", "data: {nope\n\ndata: " + frame + "\n\n", 1, 1},
		{"event without model skipped", "data: {\"event\":\"model_status\"}\n\n", 0, 1},
		{"event without a name skipped", "data: {\"model\":\"m\"}\n\n", 0, 1},
		{"empty stream", "", 0, 0},
		{"blank lines only", "\n\n\n", 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, skipped, err := collect(t, c.stream)
			if err != nil || len(got) != c.want || skipped != c.skipped {
				t.Errorf("got %d events, %d skipped, err %v; want %d, %d", len(got), skipped, err, c.want, c.skipped)
			}
		})
	}
}

func TestReadEventsValueIsClamped(t *testing.T) {
	for _, c := range []struct {
		in   string
		want float64
	}{{"1.7", 1}, {"-0.5", 0}, {"0.25", 0.25}} {
		stream := `data: {"model":"m","event":"status_change","data":{"status":"loading","progress":{"value":` + c.in + `}}}` + "\n\n"
		got, _, err := collect(t, stream)
		if err != nil || len(got) != 1 || !got[0].HasValue || got[0].Value != c.want {
			t.Errorf("value %s: %+v %v, want %v", c.in, got, err, c.want)
		}
	}
	// A progress object with no value reports no progress.
	got, _, _ := collect(t, `data: {"model":"m","event":"status_change","data":{"status":"loading","progress":{"stages":["a"]}}}`+"\n\n")
	if len(got) != 1 || got[0].HasValue {
		t.Errorf("no value: %+v", got)
	}
}

func TestReadEventsStopsWhenAskedAndOnHugeLine(t *testing.T) {
	frame := `data: {"model":"m","event":"model_remove"}` + "\n\n"
	n := 0
	_, err := readEvents(strings.NewReader(frame+frame+frame), func(Progress) bool { n++; return n < 2 })
	if err != nil || n != 2 {
		t.Errorf("emit false must stop the reader: n=%d err=%v", n, err)
	}
	huge := "data: " + strings.Repeat("x", maxSSELine+10) + "\n\n"
	if _, _, err := collect(t, huge); !errors.Is(err, ErrMalformed) {
		t.Errorf("a huge line gave %v, want ErrMalformed", err)
	}
}

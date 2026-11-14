package music_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/music"
)

func TestHTTPShim(t *testing.T) {
	var lastBody map[string]any
	health := `{"status":"ready","model_id":"music-example","device":"cuda","dtype":"bf16","vram_mib":1234,"error":null}`
	submit := http.StatusAccepted
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/health":
			_, _ = io.WriteString(w, health)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tasks":
			_ = json.NewDecoder(r.Body).Decode(&lastBody)
			w.WriteHeader(submit)
			_, _ = io.WriteString(w, `{"task_id":"tsk_000000000001","error":"queue_full"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks/tsk_000000000001":
			_, _ = io.WriteString(w, `{"state":"done","progress":1,"error":null,"outputs":[{"path":"/w/a.wav","duration_s":60.1,"sample_rate":48000,"mime":"audio/wav"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tasks/tsk_000000000001/cancel":
			_, _ = io.WriteString(w, `{}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"not_found"}`)
		}
	}))
	defer srv.Close()
	c := &music.HTTPShim{BaseURL: func() string { return srv.URL }, HTTP: srv.Client()}
	ctx := context.Background()

	h, err := c.Health(ctx)
	if err != nil || h.Status != music.HealthReady || h.ModelID != "music-example" || *h.VRAMMiB != 1234 {
		t.Fatalf("health = %+v, %v", h, err)
	}
	if st, model := (music.HealthProbe{Shim: c}).EngineStatus(); st != music.EngineReady || model != "music-example" {
		t.Fatalf("probe = %s %s", st, model)
	}
	health = `{"status":"loading","model_id":"music-example","device":"cuda","dtype":"bf16","vram_mib":null,"error":null}`
	if st, _ := (music.HealthProbe{Shim: c}).EngineStatus(); st != music.EngineStarting {
		t.Fatalf("probe while loading = %s", st)
	}
	health = `{"status":"error","model_id":"music-example","device":"cuda","dtype":"bf16","vram_mib":null,"error":"x"}`
	if st, _ := (music.HealthProbe{Shim: c}).EngineStatus(); st != music.EngineFailed {
		t.Fatalf("probe after a failed load = %s", st)
	}

	seed := int64(3)
	id, err := c.Submit(ctx, music.TaskRequest{Lyrics: "la", DurationS: 30, Variations: 1, Seed: &seed, OutputDir: "/w/op"})
	if err != nil || id != "tsk_000000000001" || lastBody["output_dir"] != "/w/op" || lastBody["sample_path"] != nil {
		t.Fatalf("submit = %q, %v, body %v", id, err, lastBody)
	}
	for status, want := range map[int]error{http.StatusTooManyRequests: music.ErrQueueFull, http.StatusServiceUnavailable: music.ErrNotReady} {
		submit = status
		if _, err := c.Submit(ctx, music.TaskRequest{}); !errors.Is(err, want) {
			t.Fatalf("submit %d: %v", status, err)
		}
	}
	submit = http.StatusBadRequest
	if _, err := c.Submit(ctx, music.TaskRequest{}); err == nil {
		t.Fatal("a 400 must be an error")
	}

	st, err := c.Task(ctx, "tsk_000000000001")
	if err != nil || st.State != music.TaskDone || len(st.Outputs) != 1 || st.Outputs[0].DurationS != 60.1 {
		t.Fatalf("task = %+v, %v", st, err)
	}
	if _, err := c.Task(ctx, "tsk_unknown"); !errors.Is(err, music.ErrTaskNotFound) {
		t.Fatalf("unknown task: %v", err)
	}
	if err := c.Cancel(ctx, "tsk_000000000001"); err != nil {
		t.Fatal(err)
	}
	if err := c.Cancel(ctx, "tsk_unknown"); !errors.Is(err, music.ErrTaskNotFound) {
		t.Fatalf("cancel unknown: %v", err)
	}

	srv.Close()
	if _, err := c.Health(ctx); !errors.Is(err, music.ErrUnreachable) {
		t.Fatalf("closed server: %v", err)
	}
	if st, model := (music.HealthProbe{Shim: c}).EngineStatus(); st != music.EngineStopped || model != "" {
		t.Fatalf("probe of a stopped engine = %s %q", st, model)
	}
	if strings.Contains(srv.URL, "0.0.0.0") {
		t.Fatal("the test server must be on loopback")
	}
}

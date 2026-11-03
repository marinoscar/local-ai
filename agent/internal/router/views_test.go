package router

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

func TestModelDirAndFilePath(t *testing.T) {
	if got := ModelDir("/srv/localai/", "m-a"); got != "/srv/localai/models/m-a" {
		t.Errorf("ModelDir = %q", got)
	}
	p, err := ModelFilePath("/srv/localai", "m-a", "main/weights.gguf")
	if err != nil || p != "/srv/localai/models/m-a/main/weights.gguf" {
		t.Errorf("ModelFilePath = %q, %v", p, err)
	}
	for _, bad := range []string{"", "/etc/passwd", "../x", "a/../../x", "a//b"} {
		if _, err := ModelFilePath("/srv/localai", "m-a", bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestRenderSectionIsWhatRenderWrites(t *testing.T) {
	p := Preset{ID: "m-a", Pairs: []Pair{{"model", "/srv/localai/models/m-a/w.gguf"}, {"ctx-size", "4096"}}}
	want := "[m-a]\nmodel = /srv/localai/models/m-a/w.gguf\nctx-size = 4096\n"
	if got := RenderSection(p, false); got != want {
		t.Errorf("section = %q", got)
	}
	if got := RenderSection(p, true); got != want+"load-on-startup = true\n" {
		t.Errorf("default section = %q", got)
	}
	// The whole file is the header, the defaults and these sections, so a view cannot disagree with it.
	file, err := Render(Header{AgentVersion: "t", At: time.Date(2026, 11, 2, 15, 4, 5, 0, time.UTC)}, []Preset{p}, "m-a")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(file), "\n"+RenderSection(p, true)) {
		t.Errorf("Render does not contain the section:\n%s", file)
	}
}

func TestAPIErrorKeepsTheOrchestratorsCodeStatusAndDetails(t *testing.T) {
	oe := &OpError{Code: CodeLlamaDown, Status: http.StatusInternalServerError, Message: "m", Details: map[string]any{"op_id": "op_x"}}
	err := APIError("en", oe)
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != doctor.LLM001 || ae.Status != http.StatusInternalServerError || ae.Details["op_id"] != "op_x" {
		t.Errorf("error = %#v", err)
	}
	if got := APIError("en", ErrInterrupted); !errors.As(got, &ae) || ae.Code != doctor.AGT024 || ae.Status != http.StatusServiceUnavailable {
		t.Errorf("interrupted = %#v", got)
	}
	plain := errors.New("a bug")
	if got := APIError("en", plain); got != plain {
		t.Errorf("a plain error is returned as it is, got %#v", got)
	}
}

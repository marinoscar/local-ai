package router

import (
	"errors"
	"testing"
	"time"
)

func TestParseV1Models(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    []string // "id|alias,alias|status"
		wantErr bool
	}{
		{"one model with aliases", `{"object":"list","data":[{"id":"m-a","aliases":["m-a","m-a-old"],"tags":["chat"],"object":"model","owned_by":"llamacpp"}]}`,
			[]string{"m-a|m-a-old|loaded"}, false},
		{"older build: no aliases at all", `{"data":[{"id":"m-a","object":"model"}],"models":[{"name":"x"}]}`, []string{"m-a||loaded"}, false},
		{"a router answers with status", `{"data":[{"id":"m-a","status":{"value":"unloaded"}},{"id":"m-b","status":{"value":"loaded"}}]}`,
			[]string{"m-a||unloaded", "m-b||loaded"}, false},
		{"empty list", `{"data":[]}`, nil, false},
		{"not an object", `[]`, nil, true},
		{"no data array", `{"models":[]}`, nil, true},
		{"entry without an id", `{"data":[{"object":"model"}]}`, nil, true},
		{"duplicate id", `{"data":[{"id":"m-a"},{"id":"m-a"}]}`, nil, true},
		{"trailing data", `{"data":[]} {"data":[]}`, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseV1Models([]byte(c.body))
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if c.wantErr {
				if !errors.Is(err, ErrMalformed) {
					t.Fatalf("err = %v, want ErrMalformed", err)
				}
				return
			}
			var lines []string
			for _, m := range got {
				al := ""
				for i, a := range m.Aliases {
					if i > 0 {
						al += ","
					}
					al += a
				}
				lines = append(lines, m.ID+"|"+al+"|"+string(m.Status))
			}
			eqStrings(t, "models", lines, c.want)
		})
	}
}

func TestModelStatusServes(t *testing.T) {
	m := ModelStatus{ID: "qwen3.5-9b", Aliases: []string{"qwen3.5-9b-q4km"}}
	for id, want := range map[string]bool{"qwen3.5-9b": true, "qwen3.5-9b-q4km": true, "other": false, "": false} {
		if got := m.serves(id); got != want {
			t.Errorf("serves(%q) = %v, want %v", id, got, want)
		}
	}
}

// TestClientModelsFollowsMode: with Options.Mode single, Models reads GET /v1/models of the one-model
// server; in router mode it reads GET /models.
func TestClientModelsFollowsMode(t *testing.T) {
	r := newSingleRig(t)
	r.srv.set(true, LlamaModeSingle, srvModel{ID: idQwen, Aliases: []string{idQwen, "qwen3.5-9b"}})

	r.setMode(LlamaModeSingle)
	got, err := r.client.Models(bg)
	if err != nil || len(got) != 1 || got[0].ID != idQwen || !got[0].Loaded() {
		t.Fatalf("single Models = %+v, %v", got, err)
	}
	if r.srv.count("GET /v1/models") != 1 || r.srv.count("GET /models") != 0 {
		t.Fatalf("requests = %q", r.srv.requests())
	}

	// In router mode the same one-model answer has no status, so it is not a router list.
	r.setMode(LlamaModeRouter)
	if _, err := r.client.Models(bg); !errors.Is(err, ErrMalformed) {
		t.Fatalf("router Models of a one-model server = %v, want ErrMalformed", err)
	}
	r.srv.set(true, LlamaModeRouter, srvModel{ID: idQwen, Status: "loaded"}, srvModel{ID: idVLM, Status: "unloaded"})
	if got, err := r.client.Models(bg); err != nil || len(got) != 2 {
		t.Fatalf("router Models = %+v, %v", got, err)
	}
}

func TestClientServerMode(t *testing.T) {
	r := newSingleRig(t)
	cases := []struct {
		name    string
		healthy bool
		kind    string
		want    string
		down    bool
	}{
		{"a one-model server", true, LlamaModeSingle, LlamaModeSingle, false},
		{"a router", true, LlamaModeRouter, LlamaModeRouter, false},
		{"not answering", false, LlamaModeSingle, "", true},
	}
	for _, c := range cases {
		r.srv.set(c.healthy, c.kind, srvModel{ID: idQwen, Status: "loaded"})
		got, err := r.client.ServerMode(bg)
		if c.down {
			if !errors.Is(err, ErrDown) {
				t.Errorf("%s: err = %v, want ErrDown", c.name, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: ServerMode = %q, %v, want %q", c.name, got, err, c.want)
		}
	}
}

// TestSingleLoadTimeoutIsVirtual: the 300 s load timeout of a model that never answers takes no real
// time, and the clock shows it was waited for.
func TestSingleLoadTimeoutIsVirtual(t *testing.T) {
	r := newSingleRig(t)
	r.boot(idQwen)
	r.sys.crash[idVLM] = true
	start := r.clk.Now()
	realStart := time.Now()
	_, err := r.load(idVLM)
	if oe := opErrOf(t, err); oe.Code != CodeLoadTimeout {
		t.Fatalf("error = %v", oe)
	}
	if virt := r.clk.Now().Sub(start); virt < 300*time.Second {
		t.Fatalf("the load gave up after %s of virtual time", virt)
	}
	if real := time.Since(realStart); real > 30*time.Second {
		t.Fatalf("the test took %s of real time", real)
	}
}

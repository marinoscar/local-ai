package cli

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
)

const profileNow = `{"profile":"small-8gb","display_name":"Small GPU laptop","overrides":{"add_models":["extra-model-q4km"],"remove_content":["gutenberg-en"]},` +
	`"suggested":"small-8gb","match_reasons":[],"effective":{"models":["qwen3.5-9b-q4km"],"content":["wikipedia-en-nopic"],"planned_bytes":1,"budget_bytes":1}}`

const profileCatalog = `{"version":"2026.11.02-1","source":"cloud",` +
	`"models":[{"id":"qwen3.5-9b-q4km","display_name":"Qwen 3.5 9B","size_bytes":5868826976},{"id":"small-vlm-q4km","display_name":"Tiny","size_bytes":5200000000},` +
	`{"id":"extra-model-q4km","display_name":"Extra","size_bytes":2000000000}],` +
	`"content":[{"id":"wikipedia-es-nopic","kind":"kiwix","display_name":"Wikipedia","size_bytes":12000000000},` +
	`{"id":"gutenberg-en","kind":"books","display_name":"Books","size_bytes":9000000000}],` +
	`"profiles":[{"id":"small-8gb","display_name":"Small GPU laptop","include":{"models":["qwen3.5-9b-q4km"],"content":[]},"budget_bytes":220000000000},` +
	`{"id":"cuda-12gb","display_name":"CUDA 12 GB","include":{"models":["qwen3.5-9b-q4km","small-vlm-q4km"],"content":["wikipedia-es-nopic","gutenberg-en"]},"budget_bytes":300000000000}]}`

const profileSetBody = `{"profile":"cuda-12gb","display_name":"CUDA 12 GB","overrides":{},"suggested":null,"match_reasons":[],` +
	`"effective":{"models":[],"content":[],"planned_bytes":1,"budget_bytes":1}}`

func newProfileRig(t *testing.T) *mgRig {
	r := newMgRig(t, fake.Options{})
	r.agent().Respond(http.MethodGet, "/profile", 200, profileNow)
	r.agent().Respond(http.MethodGet, "/catalog", 200, profileCatalog)
	r.agent().Respond(http.MethodPut, "/profile", 200, profileSetBody)
	return r
}

func TestProfileSet(t *testing.T) {
	const done = "✓ Profile set to cuda-12gb. Run `aictl ark sync` to fetch the new content.\n"
	t.Run("the preview lists the models and content with sizes, then asks", func(t *testing.T) {
		r := newProfileRig(t)
		got := r.run(mgArgs("profile", "set", "cuda-12gb").withTerm("y"))
		// extra-model-q4km comes from the overrides (add_models); gutenberg-en is removed by them.
		wantErr := "Profile now:  small-8gb (Small GPU laptop)\n" +
			"Switch to:    cuda-12gb (CUDA 12 GB)\n" +
			"Models (3, 13.1 GB):\n" +
			"  extra-model-q4km  2.0 GB\n" +
			"  qwen3.5-9b-q4km   5.9 GB\n" +
			"  small-vlm-q4km    5.2 GB\n" +
			"Content (1, 12.0 GB):\n" +
			"  wikipedia-es-nopic  12.0 GB\n" +
			"Total 25.1 GB of a 300 GB budget. Nothing is deleted now.\n" +
			"Switch to the profile cuda-12gb? [y/N] "
		checkRun(t, got, 0, done, wantErr)
		if b := r.lastBody(http.MethodPut, "/profile"); b != `{"profile":"cuda-12gb"}` {
			t.Errorf("body %s", b)
		}
	})
	t.Run("Spanish", func(t *testing.T) {
		r := newProfileRig(t)
		got := r.run(mgArgs("--lang", "es", "profile", "set", "cuda-12gb").withTerm("s"))
		for _, want := range []string{"Perfil actual:  small-8gb", "Cambiar a:      cuda-12gb (CUDA 12 GB)", "Modelos (3, 13.1 GB):", "Contenido (1, 12.0 GB):",
			"Total 25.1 GB de un presupuesto de 300 GB. Ahora no se borra nada.", "¿Cambiar al perfil cuda-12gb? [s/N] "} {
			if !strings.Contains(got.errOut, want) {
				t.Errorf("stderr misses %q:\n%s", want, got.errOut)
			}
		}
		if got.code != 0 || got.out != "✓ Perfil cambiado a cuda-12gb. Ejecute `aictl ark sync` para traer el contenido nuevo.\n" &&
			!strings.HasPrefix(got.out, "✓ Perfil cambiado a cuda-12gb.") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("the default answer is No: nothing is changed", func(t *testing.T) {
		r := newProfileRig(t)
		got := r.run(mgArgs("profile", "set", "cuda-12gb").withTerm(""))
		if got.code != 0 || got.out != "" || r.agent().Count("/profile") != 1 { // only the GET
			t.Fatalf("%+v, requests %v", got, r.requestLines())
		}
		for _, q := range r.requestLines() {
			if strings.HasPrefix(q, "PUT") {
				t.Errorf("a PUT was sent")
			}
		}
	})
	t.Run("--yes skips the preview and the reads", func(t *testing.T) {
		r := newProfileRig(t)
		got := r.run(mgArgs("-y", "profile", "set", "cuda-12gb"))
		checkRun(t, got, 0, done, "")
		if strings.Join(r.requestLines(), ",") != "PUT /profile" {
			t.Errorf("requests: %v", r.requestLines())
		}
	})
	t.Run("no terminal and no --yes: LAI-CLI-007 before any request", func(t *testing.T) {
		r := newProfileRig(t)
		got := r.run(mgArgs("profile", "set", "cuda-12gb"))
		if got.code != ExitUsage || !strings.Contains(got.errOut, "[LAI-CLI-007]") || len(r.requestLines()) != 0 {
			t.Fatalf("%+v, requests %v", got, r.requestLines())
		}
	})
	t.Run("--json needs --yes and prints the body", func(t *testing.T) {
		r := newProfileRig(t)
		if got := r.run(mgArgs("--json", "profile", "set", "cuda-12gb")); got.code != ExitUsage {
			t.Fatalf("without --yes: %+v", got)
		}
		checkRun(t, r.run(mgArgs("--json", "-y", "profile", "set", "cuda-12gb")), 0, profileSetBody+"\n", "")
	})
	t.Run("already the profile in use", func(t *testing.T) {
		r := newProfileRig(t)
		got := r.run(mgArgs("profile", "set", "small-8gb").withTerm("y"))
		checkRun(t, got, 0, "i The profile is already small-8gb.\n", "")
		for _, q := range r.requestLines() {
			if strings.HasPrefix(q, "PUT") {
				t.Errorf("a PUT was sent")
			}
		}
	})
	t.Run("a profile the catalog does not have: exit 6, nothing changed", func(t *testing.T) {
		r := newProfileRig(t)
		got := r.run(mgArgs("profile", "set", "no-such-profile").withTerm("y"))
		if got.code != ExitRefused || !strings.Contains(got.errOut, "[LAI-CAT-003]") || !strings.Contains(got.errOut, "aictl profile suggest") {
			t.Fatalf("%+v", got)
		}
		for _, q := range r.requestLines() {
			if strings.HasPrefix(q, "PUT") {
				t.Errorf("a PUT was sent")
			}
		}
	})
	t.Run("a catalog that does not list its profiles: ask without the lists", func(t *testing.T) {
		r := newProfileRig(t)
		r.agent().Respond(http.MethodGet, "/catalog", 200, `{"version":"x","source":"cloud","models":[],"content":[]}`)
		got := r.run(mgArgs("profile", "set", "cuda-12gb").withTerm("y"))
		if got.code != 0 || !strings.Contains(got.errOut, "Switch to:    cuda-12gb\n") || strings.Contains(got.errOut, "Models (") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("the agent refuses the profile: exit 6", func(t *testing.T) {
		for _, code := range []string{"LAI-CAT-003", "LAI-CAT-004"} {
			r := newProfileRig(t)
			r.agent().RespondError(http.MethodPut, "/profile", 422, code, "Invalid.", "Run `aictl profile suggest`.", nil)
			got := r.run(mgArgs("-y", "profile", "set", "cuda-12gb"))
			if got.code != ExitRefused || !strings.Contains(got.errOut, "["+code+"]") {
				t.Errorf("%s: %+v", code, got)
			}
		}
	})
	t.Run("a malformed id is exit 2 and asks nothing", func(t *testing.T) {
		for _, a := range [][]string{{"Bad Id"}, {"../x"}, {}, {"a", "b"}} {
			r := newProfileRig(t)
			got := r.run(mgArgs(append([]string{"profile", "set"}, a...)...))
			if got.code != ExitUsage || len(r.requestLines()) != 0 {
				t.Errorf("%v: %+v", a, got)
			}
		}
	})
	t.Run("an operator is asked for the PIN after the confirmation", func(t *testing.T) {
		a := newAdminAgent(t)
		a.h.agent.Respond(http.MethodGet, "/profile", 200, profileNow)
		a.h.agent.Respond(http.MethodGet, "/catalog", 200, profileCatalog)
		a.h.agent.Handle(http.MethodPut, "/profile", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-LocalAI-Pin-Grant") == testGrant {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, profileSetBody)
				return
			}
			fake.WriteEnvelope(w, 403, api.CodeAdminRequired, "This action needs the admin PIN.", "Enter the admin PIN.", map[string]any{"pin_required": true})
		})
		r := &mgRig{t: t, h: a.h}
		got := r.run(mgArgs("profile", "set", "cuda-12gb").withTerm("y", testPIN))
		if got.code != 0 || a.verifies() != 1 || !strings.HasPrefix(got.out, "✓ Profile set to cuda-12gb.") {
			t.Fatalf("%+v verifies %d", got, a.verifies())
		}
		secretFree(t, testPIN, got.out, got.errOut)
	})
}

func TestApplyOverrides(t *testing.T) {
	over := map[string]any{"add_models": []any{"b", "a", "x"}, "remove_models": []any{"x"}}
	got := applyOverrides([]string{"c", "a"}, over, "models")
	if strings.Join(got, ",") != "a,b,c" {
		t.Errorf("got %v", got)
	}
	if got := applyOverrides([]string{"k"}, nil, "content"); len(got) != 1 || got[0] != "k" {
		t.Errorf("nil overrides: %v", got)
	}
}

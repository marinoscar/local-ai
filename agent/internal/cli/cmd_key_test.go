package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/user"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	xfake "github.com/marinoscar/local-ai/agent/internal/execx/fake"
)

// mgKeyBody is GET /key as agent.md section 3.8.13 shows it (placeholder host names only).
func mgKeyBody() string {
	return fmt.Sprintf(`{"key":%q,"fingerprint":"c0de…c0de",`+
		`"base_urls":{"local":"http://127.0.0.1:8080/v1","tailnet":"https://node-a.example.test/v1","public":null},`+
		`"models":["qwen3.5-9b-q4km","small-vlm-q4km"],`+
		`"snippets":{"curl":"curl -s http://127.0.0.1:8080/v1/chat/completions -H \"Authorization: Bearer $KEY\" -H 'Content-Type: application/json' -d '{\"model\":\"qwen3.5-9b-q4km\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello\"}]}'",`+
		`"env":"OPENAI_BASE_URL=http://127.0.0.1:8080/v1\nOPENAI_API_KEY=<key>",`+
		`"opencode":{"provider":{"local-llama":{"options":{"baseURL":"http://127.0.0.1:8080/v1","apiKey":"{file:~/.config/opencode/llama-api-key}"}}}},`+
		`"openai_python":"from openai import OpenAI\nclient = OpenAI(base_url=\"http://127.0.0.1:8080/v1\", api_key=KEY)"}}`, mgKey())
}

func newKeyRig(t *testing.T) *mgRig {
	r := newMgRig(t, fake.Options{})
	r.agent().Respond(http.MethodGet, "/key", 200, mgKeyBody())
	return r
}

const keyWarning = "! This is the secret key for this node. Do not paste it in chats or emails.\n"

func TestKeyShowHuman(t *testing.T) {
	want := "Key          " + mgKey() + "\n" +
		"Fingerprint  c0de…c0de\n" +
		"Local URL    http://127.0.0.1:8080/v1\n" +
		"Tailnet URL  https://node-a.example.test/v1\n" +
		"Public URL   not available\n" +
		"Models       qwen3.5-9b-q4km, small-vlm-q4km\n"
	t.Run("en", func(t *testing.T) {
		checkRun(t, newKeyRig(t).run(mgArgs("key", "show")), 0, want, keyWarning)
	})
	t.Run("es", func(t *testing.T) {
		wantEs := "Clave        " + mgKey() + "\n" +
			"Huella       c0de…c0de\n" +
			"URL local    http://127.0.0.1:8080/v1\n" +
			"URL tailnet  https://node-a.example.test/v1\n" +
			"URL pública  no disponible\n" +
			"Modelos      qwen3.5-9b-q4km, small-vlm-q4km\n"
		checkRun(t, newKeyRig(t).run(mgArgs("--lang", "es", "key", "show")), 0, wantEs,
			"! Esta es la clave secreta de este nodo. No la pegue en chats ni correos.\n")
	})
	t.Run("quiet keeps the key and the warning", func(t *testing.T) {
		got := newKeyRig(t).run(mgArgs("-q", "key", "show"))
		if got.code != 0 || !strings.Contains(got.out, mgKey()) || got.errOut != keyWarning {
			t.Fatalf("%+v", got)
		}
	})
}

// --json is the body of the agent, one document, nothing on stderr.
func TestKeyShowJSON(t *testing.T) {
	got := newKeyRig(t).run(mgArgs("--json", "key", "show"))
	checkRun(t, got, 0, mgKeyBody()+"\n", "")
}

func TestKeyShowFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		code   string
		exit   int
	}{
		{"not an operator", 403, "LAI-SEC-013", ExitDenied},
		{"no credential", 401, api.CodeUnauthenticated, ExitDenied},
		{"agent bug", 500, "LAI-AGT-099", ExitFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newMgRig(t, fake.Options{})
			r.agent().RespondError(http.MethodGet, "/key", tc.status, tc.code, "Refused.", "Do the right thing.", nil)
			got := r.run(mgArgs("key", "show"))
			if got.code != tc.exit || !strings.Contains(got.errOut, "["+tc.code+"]") || got.out != "" {
				t.Fatalf("%+v", got)
			}
			secretFree(t, mgKey(), got.out, got.errOut)
		})
	}
	t.Run("agent down", func(t *testing.T) {
		r := newMgRig(t, fake.Options{NoSocket: true, NoTCP: true})
		got := r.run(mgArgs("key", "show"))
		if got.code != ExitUnreachable || !strings.Contains(got.errOut, "[LAI-CLI-001]") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("agent down with --json is the envelope", func(t *testing.T) {
		r := newMgRig(t, fake.Options{NoSocket: true, NoTCP: true})
		got := r.run(mgArgs("--json", "key", "show"))
		if got.code != ExitUnreachable || got.errOut != "" || !strings.Contains(got.out, `"code":"LAI-CLI-001"`) {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("an answer without a key", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.agent().Respond(http.MethodGet, "/key", 200, `{"fingerprint":"x"}`)
		got := r.run(mgArgs("key", "show"))
		if got.code != ExitFailed || !strings.Contains(got.errOut, "[LAI-CLI-009]") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("an argument is a usage error", func(t *testing.T) {
		got := newKeyRig(t).run(mgArgs("key", "show", "now"))
		if got.code != ExitUsage || !strings.Contains(got.errOut, "[LAI-CLI-005]") {
			t.Fatalf("%+v", got)
		}
	})
}

// A closed pipe on stdout (aictl key show | head -c 0) is not a failure.
func TestKeyShowClosedPipe(t *testing.T) {
	r := newKeyRig(t)
	o := mgArgs("key", "show")
	env := r.env(o)
	env.Stdout, env.Stderr = &brokenPipe{}, io.Discard
	if code := Run(context.Background(), env, mgRegistry(), o.args); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
}

// With LOCALAI_CLI_DEBUG the request and the answer are logged; neither may show the key.
func TestKeyShowDebugLogHidesTheKey(t *testing.T) {
	r := newKeyRig(t)
	got := r.run(mgArgs("key", "show").setenv("LOCALAI_CLI_DEBUG", "1"))
	if got.code != 0 {
		t.Fatalf("%+v", got)
	}
	if strings.Count(got.errOut, mgKey()) != 0 {
		t.Fatalf("the debug log shows the key:\n%s", got.errOut)
	}
	if !strings.Contains(got.errOut, `"path":"/api/v1/key"`) {
		t.Fatalf("no debug log was written:\n%s", got.errOut)
	}
}

// --- key copy -------------------------------------------------------------------------------------

// clipRig replaces the runner, the program lookup and the user lookup of the clipboard code.
type clipRig struct {
	runner *xfake.Runner
}

func newClipRig(t *testing.T, installed ...string) *clipRig {
	t.Helper()
	cr := &clipRig{runner: xfake.New()}
	oldRun, oldLook, oldUser := clipRunner, clipLookPath, clipLookupUser
	t.Cleanup(func() { clipRunner, clipLookPath, clipLookupUser = oldRun, oldLook, oldUser })
	clipRunner = cr.runner
	clipLookPath = func(name string) (string, error) {
		for _, n := range installed {
			if n == name {
				return "/usr/bin/" + name, nil
			}
		}
		return "", execx.ErrNotFound
	}
	clipLookupUser = func(name string) (*user.User, error) {
		if name == "person-a" {
			return &user.User{Uid: "1000", Gid: "1000", Username: name, HomeDir: "/home/person-a"}, nil
		}
		return nil, errors.New("no such user")
	}
	return cr
}

func (cr *clipRig) ok(path string, args ...string) {
	cr.runner.Expect(xfake.Key(path, args...), execx.Result{}, nil)
}

func (cr *clipRig) fails(path string, args ...string) {
	cr.runner.Expect(xfake.Key(path, args...), execx.Result{ExitCode: 1}, nil)
}

func TestKeyCopy(t *testing.T) {
	// The style guide wraps at 80 columns, so the spec's one-line example is two lines.
	const okLine = "✓ The API key is on the clipboard. Paste it where it is needed, then copy\n  something else.\n"
	t.Run("Wayland: wl-copy, the key on stdin, nothing printed", func(t *testing.T) {
		cr := newClipRig(t, "wl-copy", "xclip")
		cr.ok("/usr/bin/wl-copy")
		r := newKeyRig(t)
		got := r.run(mgArgs("key", "copy").setenv("WAYLAND_DISPLAY", "wayland-1").setenv("DISPLAY", ":0"))
		checkRun(t, got, 0, okLine, "")
		calls := cr.runner.Calls()
		if len(calls) != 1 || calls[0].Path != "/usr/bin/wl-copy" || len(calls[0].Args) != 0 {
			t.Fatalf("calls: %+v", calls)
		}
		stdin, _ := io.ReadAll(calls[0].Stdin)
		if string(stdin) != mgKey() {
			t.Errorf("the tool did not get the key on stdin: %q", stdin)
		}
		for _, call := range calls {
			secretFree(t, mgKey(), call.Path, strings.Join(call.Args, " "), strings.Join(call.Env, " "))
		}
		if !contains(calls[0].Env, "WAYLAND_DISPLAY=wayland-1") || contains(calls[0].Env, "XDG_RUNTIME_DIR=/run/user/1000") {
			t.Errorf("env: %v", calls[0].Env)
		}
	})
	t.Run("X11: xclip after a failing wl-copy, then xsel", func(t *testing.T) {
		cr := newClipRig(t, "wl-copy", "xclip", "xsel")
		cr.fails("/usr/bin/wl-copy")
		cr.fails("/usr/bin/xclip", "-selection", "clipboard")
		cr.ok("/usr/bin/xsel", "--clipboard", "--input")
		got := newKeyRig(t).run(mgArgs("--json", "key", "copy").setenv("WAYLAND_DISPLAY", "wayland-1").setenv("DISPLAY", ":0"))
		checkRun(t, got, 0, `{"copied":true,"method":"xsel"}`+"\n", "")
		var names []string
		for _, c := range cr.runner.Calls() {
			names = append(names, strings.TrimPrefix(c.Path, "/usr/bin/"))
			secretFree(t, mgKey(), strings.Join(c.Args, " "))
		}
		if strings.Join(names, ",") != "wl-copy,xclip,xsel" {
			t.Errorf("order: %v", names)
		}
	})
	t.Run("a tool that is not installed is skipped", func(t *testing.T) {
		cr := newClipRig(t, "xsel")
		cr.ok("/usr/bin/xsel", "--clipboard", "--input")
		got := newKeyRig(t).run(mgArgs("--json", "key", "copy").setenv("DISPLAY", ":0"))
		checkRun(t, got, 0, `{"copied":true,"method":"xsel"}`+"\n", "")
	})
	t.Run("OSC 52 on a terminal when no tool works", func(t *testing.T) {
		newClipRig(t)
		r := newKeyRig(t)
		o := mgArgs("key", "copy")
		o.tty = true
		got := r.run(o)
		b64 := "\x1b]52;c;"
		if got.code != 0 || !strings.HasPrefix(got.out, b64) || !strings.Contains(got.out, "The API key is on the clipboard.") {
			t.Fatalf("%+v", got)
		}
		secretFree(t, mgKey(), got.out, got.errOut)
	})
	t.Run("OSC 52 never mixes into --json", func(t *testing.T) {
		newClipRig(t)
		o := mgArgs("--json", "key", "copy")
		o.tty = true
		got := newKeyRig(t).run(o)
		if got.code != ExitFailed || strings.Contains(got.out, "\x1b") || !strings.Contains(got.out, `"code":"LAI-CLI-011"`) {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("nothing available is LAI-CLI-011, exit 1, no key", func(t *testing.T) {
		newClipRig(t)
		got := newKeyRig(t).run(mgArgs("key", "copy"))
		want := "! No clipboard tool is available.  [LAI-CLI-011]\n" +
			"  → Fix: Use `aictl key show` and copy the key by hand.\n"
		checkRun(t, got, ExitFailed, "", want)
	})
	t.Run("Spanish", func(t *testing.T) {
		cr := newClipRig(t, "wl-copy")
		cr.ok("/usr/bin/wl-copy")
		got := newKeyRig(t).run(mgArgs("--lang", "es", "key", "copy").setenv("WAYLAND_DISPLAY", "wayland-1"))
		checkRun(t, got, 0, "✓ La clave de la API está en el portapapeles. Péguela donde la necesite y luego\n  copie otra cosa.\n", "")
	})
	t.Run("under sudo the tool runs as the person at the desktop", func(t *testing.T) {
		cr := newClipRig(t, "wl-copy")
		cr.ok("/usr/bin/wl-copy")
		o := mgArgs("key", "copy").setenv("WAYLAND_DISPLAY", "wayland-1").setenv("SUDO_USER", "person-a")
		o.euid = 0
		got := newKeyRig(t).run(o)
		if got.code != 0 {
			t.Fatalf("%+v", got)
		}
		c := cr.runner.Calls()[0]
		if c.Credential == nil || c.Credential.UID != 1000 || c.Credential.GID != 1000 {
			t.Fatalf("credential: %+v", c.Credential)
		}
		if !contains(c.Env, "XDG_RUNTIME_DIR=/run/user/1000") || !contains(c.Env, "HOME=/home/person-a") {
			t.Errorf("env: %v", c.Env)
		}
	})
	t.Run("sudo as an unknown user runs as the caller", func(t *testing.T) {
		cr := newClipRig(t, "wl-copy")
		cr.ok("/usr/bin/wl-copy")
		o := mgArgs("key", "copy").setenv("WAYLAND_DISPLAY", "wayland-1").setenv("SUDO_USER", "ghost")
		o.euid = 0
		newKeyRig(t).run(o)
		if c := cr.runner.Calls()[0]; c.Credential != nil {
			t.Errorf("credential: %+v", c.Credential)
		}
	})
	t.Run("the agent refuses", func(t *testing.T) {
		newClipRig(t)
		r := newMgRig(t, fake.Options{})
		r.agent().RespondError(http.MethodGet, "/key", 403, "LAI-SEC-013", "No.", "Join the group.", nil)
		got := r.run(mgArgs("key", "copy").setenv("WAYLAND_DISPLAY", "wayland-1"))
		if got.code != ExitDenied {
			t.Fatalf("%+v", got)
		}
	})
}

// --- key rotate -----------------------------------------------------------------------------------

const rotatedBody = `{"fingerprint":"c0de…77aa","rotated_at":"2026-11-02T15:10:00Z","cloud_synced":true,` +
	`"restored_models":["qwen3.5-9b-q4km"],` +
	`"clients_to_update":["OpenCode on this computer: ~/.config/opencode/llama-api-key","Any script that uses the old key directly"],` +
	`"op_id":"op_7k2m9q4r8t1c"}`

const rotateQuestion = "Programs using the old key will stop working until you update them. Continue? [y/N] "

func TestKeyRotate(t *testing.T) {
	const golden = "✓ New key created (fingerprint c0de…77aa). The model was started again.\n" +
		"  Update these programs with the new key (`aictl key copy`):\n" +
		"   • OpenCode on this computer: ~/.config/opencode/llama-api-key\n" +
		"   • Any script that uses the old key directly\n" +
		"  The cloud and the gateway were updated automatically.\n"
	t.Run("confirmed", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.agent().Respond(http.MethodPost, "/key/rotate", 200, rotatedBody)
		got := r.run(mgArgs("key", "rotate").withTerm("y"))
		checkRun(t, got, 0, golden, rotateQuestion)
		if body := r.lastBody(http.MethodPost, "/key/rotate"); body != `{"restart":true}` {
			t.Errorf("body %s", body)
		}
	})
	t.Run("--yes asks nothing", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.agent().Respond(http.MethodPost, "/key/rotate", 200, rotatedBody)
		checkRun(t, r.run(mgArgs("-y", "key", "rotate")), 0, golden, "")
	})
	t.Run("Spanish", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.agent().Respond(http.MethodPost, "/key/rotate", 200, rotatedBody)
		want := "✓ Clave nueva creada (huella c0de…77aa). El modelo se inició de nuevo.\n" +
			"  Actualice estos programas con la clave nueva (`aictl key copy`):\n" +
			"   • OpenCode on this computer: ~/.config/opencode/llama-api-key\n" +
			"   • Any script that uses the old key directly\n" +
			"  La nube y la pasarela se actualizaron solas.\n"
		checkRun(t, r.run(mgArgs("-y", "--lang", "es", "key", "rotate")), 0, want, "")
	})
	t.Run("the default answer is No", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.agent().Respond(http.MethodPost, "/key/rotate", 200, rotatedBody)
		got := r.run(mgArgs("key", "rotate").withTerm(""))
		if got.code != 0 || got.out != "" || len(r.requestLines()) != 0 {
			t.Fatalf("%+v, requests %v", got, r.requestLines())
		}
	})
	t.Run("no terminal and no --yes", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		got := r.run(mgArgs("key", "rotate"))
		if got.code != ExitUsage || !strings.Contains(got.errOut, "[LAI-CLI-007]") || len(r.requestLines()) != 0 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("--json is the body", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.agent().Respond(http.MethodPost, "/key/rotate", 200, rotatedBody)
		checkRun(t, r.run(mgArgs("--json", "-y", "key", "rotate")), 0, rotatedBody+"\n", "")
	})
	t.Run("the cloud was not updated", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.agent().Respond(http.MethodPost, "/key/rotate", 200, strings.Replace(rotatedBody, `"cloud_synced":true`, `"cloud_synced":false`, 1))
		got := r.run(mgArgs("-y", "key", "rotate"))
		if got.code != 0 || !strings.Contains(got.errOut, "aictl cloud resend-key") || strings.Contains(got.out, "updated automatically") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("the model did not restart: exit 1, the key is rotated", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.agent().RespondError(http.MethodPost, "/key/rotate", 500, "LAI-LLM-001", "The AI model service is not running.",
			"Run `aictl restart llama`.", map[string]any{"key_rotated": true})
		got := r.run(mgArgs("-y", "key", "rotate"))
		if got.code != ExitFailed || !strings.Contains(got.errOut, "The new key is in place") || !strings.Contains(got.errOut, "[LAI-LLM-001]") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("busy is exit 6", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.agent().RespondError(http.MethodPost, "/key/rotate", 409, "LAI-AGT-022", "Another change is running.", "Wait.", nil)
		if got := r.run(mgArgs("-y", "key", "rotate")); got.code != ExitRefused {
			t.Fatalf("%+v", got)
		}
	})
}

func TestKeyRotateAskesForThePINOfAnOperator(t *testing.T) {
	a := newAdminAgent(t)
	a.h.agent.Handle(http.MethodPost, "/key/rotate", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-LocalAI-Pin-Grant") == testGrant {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, rotatedBody)
			return
		}
		fake.WriteEnvelope(w, 403, api.CodeAdminRequired, "This action needs the admin PIN.", "Enter the admin PIN.", map[string]any{"pin_required": true})
	})
	r := &mgRig{t: t, h: a.h}
	o := mgArgs("key", "rotate").withTerm("y", testPIN)
	got := r.run(o)
	if got.code != 0 || !strings.Contains(got.out, "New key created") {
		t.Fatalf("%+v", got)
	}
	if a.verifies() != 1 {
		t.Errorf("pin verifies = %d", a.verifies())
	}
	secretFree(t, testPIN, got.out, got.errOut)
	var grants []string
	for _, q := range a.h.agent.Requests() {
		if q.Path == "/key/rotate" {
			grants = append(grants, q.Header.Get("X-LocalAI-Pin-Grant"))
		}
	}
	if len(grants) != 2 || grants[0] != "" || grants[1] != testGrant {
		t.Errorf("grants: %v", grants)
	}
	t.Run("a locked PIN is exit 4", func(t *testing.T) {
		a := newAdminAgent(t)
		a.lockAt = 1
		a.wrong = 1
		a.h.agent.RespondError(http.MethodPost, "/key/rotate", 403, api.CodeAdminRequired, "Needs the PIN.", "Enter it.", map[string]any{"pin_required": true})
		r := &mgRig{t: t, h: a.h}
		got := r.run(mgArgs("key", "rotate").withTerm("y", "0000"))
		if got.code != ExitDenied || !strings.Contains(got.errOut, "[LAI-SEC-004]") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("Ctrl-C at the confirmation is exit 130", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		term := &fake.Terminal{TTY: true, Script: []fake.Input{{Err: context.Canceled}}}
		o := mgArgs("key", "rotate")
		o.term = term
		if got := r.run(o); got.code != ExitInterrupted || got.out != "" {
			t.Fatalf("%+v", got)
		}
		if len(r.requestLines()) != 0 {
			t.Errorf("requests: %v", r.requestLines())
		}
	})
}

// --- key snippets ---------------------------------------------------------------------------------

func TestKeySnippets(t *testing.T) {
	curl := "# curl\n" +
		"export LLAMA_API_KEY=$(aictl key show --json | jq -r .key)\n" +
		"curl -s http://127.0.0.1:8080/v1/chat/completions -H \"Authorization: Bearer $LLAMA_API_KEY\" \\\n" +
		"  -H 'Content-Type: application/json' \\\n" +
		"  -d '{\"model\":\"qwen3.5-9b-q4km\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello\"}]}'\n"
	t.Run("curl alone has no header", func(t *testing.T) {
		got := newKeyRig(t).run(mgArgs("key", "snippets", "--for", "curl"))
		checkRun(t, got, 0, strings.TrimPrefix(curl, "# curl\n"), "")
	})
	t.Run("all, local, the key is a variable", func(t *testing.T) {
		got := newKeyRig(t).run(mgArgs("key", "snippets"))
		if got.code != 0 || got.errOut != "" || !strings.HasPrefix(got.out, curl) {
			t.Fatalf("%+v", got)
		}
		for _, want := range []string{
			"\n# env\nOPENAI_BASE_URL=http://127.0.0.1:8080/v1\nOPENAI_API_KEY=$LLAMA_API_KEY\n",
			"\n# opencode\n{\n  \"provider\": {\n    \"local-llama\": {",
			"\n# python\nimport os\nfrom openai import OpenAI\nclient = OpenAI(base_url=\"http://127.0.0.1:8080/v1\", api_key=os.environ[\"LLAMA_API_KEY\"])\n",
		} {
			if !strings.Contains(got.out, want) {
				t.Errorf("missing %q in:\n%s", want, got.out)
			}
		}
		secretFree(t, mgKey(), got.out, got.errOut)
	})
	t.Run("--url tailnet swaps the address", func(t *testing.T) {
		got := newKeyRig(t).run(mgArgs("key", "snippets", "--for", "env", "--url", "tailnet"))
		checkRun(t, got, 0, "OPENAI_BASE_URL=https://node-a.example.test/v1\nOPENAI_API_KEY=$LLAMA_API_KEY\n", "")
	})
	t.Run("an address the node does not have", func(t *testing.T) {
		got := newKeyRig(t).run(mgArgs("key", "snippets", "--url", "public"))
		if got.code != ExitUsage || !strings.Contains(got.errOut, "no public address") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("--with-key prints the key and the warning", func(t *testing.T) {
		got := newKeyRig(t).run(mgArgs("key", "snippets", "--for", "env", "--with-key"))
		checkRun(t, got, 0, "OPENAI_BASE_URL=http://127.0.0.1:8080/v1\nOPENAI_API_KEY="+mgKey()+"\n", keyWarning)
	})
	t.Run("--json", func(t *testing.T) {
		got := newKeyRig(t).run(mgArgs("--json", "key", "snippets", "--for", "env"))
		checkRun(t, got, 0, `{"base_url":"http://127.0.0.1:8080/v1","snippets":{"env":"OPENAI_BASE_URL=http://127.0.0.1:8080/v1\nOPENAI_API_KEY=$LLAMA_API_KEY"}}`+"\n", "")
		var doc struct {
			Snippets map[string]any `json:"snippets"`
		}
		all := newKeyRig(t).run(mgArgs("--json", "key", "snippets"))
		if err := json.Unmarshal([]byte(all.out), &doc); err != nil || len(doc.Snippets) != 4 {
			t.Fatalf("%v: %s", err, all.out)
		}
		secretFree(t, mgKey(), all.out)
	})
	t.Run("unknown --for and --url", func(t *testing.T) {
		for _, a := range [][]string{{"--for", "ruby"}, {"--url", "moon"}} {
			got := newKeyRig(t).run(mgArgs(append([]string{"key", "snippets"}, a...)...))
			if got.code != ExitUsage {
				t.Errorf("%v: %+v", a, got)
			}
		}
	})
}

package fake_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/router/fake"
)

// testBearer is 64 hex digits, built at run time.
var testBearer = strings.Repeat("abcd", 15) + "0001"

func do(t *testing.T, method, url, bearer, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func statusOf(t *testing.T, f *fake.Router, id string) string {
	t.Helper()
	_, body := do(t, "GET", f.URL()+"/models", testBearer, "")
	var out struct {
		Data []struct {
			ID     string `json:"id"`
			Status struct {
				Value    string `json:"value"`
				Failed   bool   `json:"failed"`
				ExitCode int    `json:"exit_code"`
			} `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	for _, m := range out.Data {
		if m.ID == id {
			s := m.Status.Value
			if m.Status.Failed {
				s += ":failed"
			}
			return s
		}
	}
	return "absent"
}

func TestAuthRules(t *testing.T) {
	f := fake.New(testBearer)
	defer f.Close()
	if code, _ := do(t, "GET", f.URL()+"/health", "", ""); code != 200 {
		t.Errorf("health without a key = %d", code)
	}
	if code, body := do(t, "GET", f.URL()+"/v1/models", "", ""); code != 401 || !strings.Contains(body, "Invalid API Key") {
		t.Errorf("v1/models without a key = %d %s", code, body)
	}
	if code, _ := do(t, "GET", f.URL()+"/models", "wrong", ""); code != 401 {
		t.Errorf("wrong key = %d", code)
	}
	reqs := f.Requests()
	if len(reqs) != 3 || reqs[0].HasAuth || !reqs[2].HasAuth || reqs[2].KeyOK {
		t.Errorf("request log = %+v", reqs)
	}
	for _, q := range reqs {
		if strings.Contains(q.Body+q.RawQuery+q.Path, testBearer) {
			t.Errorf("the log holds the key: %+v", q)
		}
	}
}

func TestLoadLifecycleAndCrash(t *testing.T) {
	f := fake.New(testBearer)
	defer f.Close()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	f.SetClock(func() time.Time { return now })
	f.SetLoadTime(7 * time.Second)
	f.Add(fake.Model{ID: "a"})

	if code, _ := do(t, "POST", f.URL()+"/models/load", testBearer, `{"model":"a"}`); code != 200 {
		t.Fatalf("load = %d", code)
	}
	if got := statusOf(t, f, "a"); got != "loading" {
		t.Errorf("right after load: %s", got)
	}
	now = now.Add(8 * time.Second)
	if got := statusOf(t, f, "a"); got != "loaded" {
		t.Errorf("after the load time: %s", got)
	}
	f.Crash("a", 137)
	if got := statusOf(t, f, "a"); got != "unloaded:failed" {
		t.Errorf("after a crash: %s", got)
	}
	f.FailLoad("a", 1)
	do(t, "POST", f.URL()+"/models/load", testBearer, `{"model":"a"}`)
	now = now.Add(8 * time.Second)
	if got := statusOf(t, f, "a"); got != "unloaded:failed" {
		t.Errorf("a failing load: %s", got)
	}
	if code, body := do(t, "POST", f.URL()+"/models/load", testBearer, `{"model":"zzz"}`); code != 404 {
		t.Errorf("unknown model load = %d %s", code, body)
	}
}

func TestRestartAndDownAndStreams(t *testing.T) {
	f := fake.New(testBearer)
	defer f.Close()
	f.Add(fake.Model{ID: "default", Status: fake.Loaded, LoadOnStartup: true})
	f.Add(fake.Model{ID: "other", Status: fake.Loaded})
	f.Restart()
	if got := statusOf(t, f, "default"); got != "loaded" { // zero load time: back at the next read
		t.Errorf("default after restart: %s", got)
	}
	if got := statusOf(t, f, "other"); got != "unloaded" {
		t.Errorf("other after restart: %s", got)
	}
	f.Down(true)
	if code, _ := do(t, "GET", f.URL()+"/health", "", ""); code != 503 {
		t.Errorf("down health = %d", code)
	}
	f.Down(false)
	f.Malformed("not json")
	if _, body := do(t, "GET", f.URL()+"/models", testBearer, ""); body != "not json" {
		t.Errorf("malformed body = %q", body)
	}
	// POST /models and DELETE /models are served but logged: that is how tests prove nobody sent them.
	f.Malformed("")
	do(t, "POST", f.URL()+"/models", testBearer, `{"model":"x"}`)
	if f.Calls("POST", "/models") != 1 {
		t.Error("the request log must show a POST /models")
	}
	f.SetMemory("default", `{"status":"measured"}`)
	if _, body := do(t, "GET", f.URL()+"/models", testBearer, ""); !strings.Contains(body, `"memory"`) {
		t.Error("memory not served")
	}
	f.Remove("other")
	if got := statusOf(t, f, "other"); got != "absent" {
		t.Errorf("removed model: %s", got)
	}
}

func TestStickAndRejectLoad(t *testing.T) {
	f := fake.New(testBearer)
	defer f.Close()
	f.Add(fake.Model{ID: "a", Status: fake.Loaded})

	f.Stick("a", true)
	if code, _ := do(t, "POST", f.URL()+"/models/unload", testBearer, `{"model":"a"}`); code != 200 {
		t.Fatalf("a stuck unload is still answered with success, got %d", code)
	}
	if got := statusOf(t, f, "a"); got != "loaded" {
		t.Errorf("a stuck child keeps running: %s", got)
	}
	f.Stick("a", false)
	do(t, "POST", f.URL()+"/models/unload", testBearer, `{"model":"a"}`)
	if got := statusOf(t, f, "a"); got != "unloaded" {
		t.Errorf("after Stick(false): %s", got)
	}

	f.RejectLoad("a", 500)
	if code, _ := do(t, "POST", f.URL()+"/models/load", testBearer, `{"model":"a"}`); code != 500 {
		t.Errorf("a rejected load = %d, want 500", code)
	}
	if got := statusOf(t, f, "a"); got != "unloaded" {
		t.Errorf("a rejected load starts nothing: %s", got)
	}
	f.RejectLoad("a", 0)
	if code, _ := do(t, "POST", f.URL()+"/models/load", testBearer, `{"model":"a"}`); code != 200 {
		t.Errorf("after RejectLoad(0) = %d", code)
	}
}

package cloud

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cloud/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

func TestKeyPushBodyIsExactlyKeyFingerprintAndRotatedAt(t *testing.T) {
	r := newRig(t)
	rot := time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC)
	_ = r.st.MutateSync(func(s *state.State) error { s.Key.RotatedAt = &rot; return nil })
	res := r.enroll()
	r.waitCount(fake.RouteKey, 1)
	req := r.srv.RequestsFor(fake.RouteKey)[0]
	var body map[string]string
	if err := req.JSON(&body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 3 || body["key"] != testKey || body["fingerprint"] != testKey[:4]+"…"+testKey[60:] || body["rotated_at"] != "2026-11-01T09:30:00Z" {
		t.Errorf("body keys %v (the key itself is not printed)", keysOf(body))
	}
	if !strings.HasSuffix(req.Path, "/api/nodes/"+res.ServerNodeID+"/localai/key") {
		t.Errorf("path %s", req.Path)
	}
	waitSynced(t, r, true)
}

func keysOf(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// waitSynced blocks until state.key.cloud_synced is want; the key loop writes it through the state store,
// which the rig watches, so nothing polls or sleeps.
func waitSynced(t *testing.T, r *rig, want bool) {
	t.Helper()
	for {
		ch := r.ws.changed()
		if r.st.Snapshot().Key.CloudSynced == want {
			return
		}
		select {
		case <-ch:
		case <-r.ctx.Done():
			t.Fatalf("cloud_synced never became %v", want)
		}
	}
}

func TestKeyPushRetriesEveryFiveMinutesAndWarnsAfterThirty(t *testing.T) {
	r := newRig(t)
	r.srv.Forever(fake.RouteKey, fake.Reply{Status: 503})
	r.enroll()
	r.waitCount(fake.RouteKey, 1)
	if r.st.Snapshot().Key.CloudSynced {
		t.Fatal("cloud_synced is true although the push failed")
	}
	for i := 2; i <= 7; i++ {
		r.waitTimers(3) // the heartbeat's, the state loop's and the key retry timer
		r.clk.Advance(5 * time.Minute)
		r.waitCount(fake.RouteKey, i)
	}
	// The first failure was at 0 min; the seventh attempt is at 30 min unsynced.
	r.waitAlert(doctor.CLD060, true)
	// The cloud comes back: the next retry syncs and clears the alert.
	r.srv.Clear(fake.RouteKey)
	r.waitTimers(3)
	r.clk.Advance(5 * time.Minute)
	r.waitCount(fake.RouteKey, 8)
	r.waitAlert(doctor.CLD060, false)
	waitSynced(t, r, true)
	// Once synced the loop is idle: no more key calls.
	n := r.srv.Count(fake.RouteKey)
	r.clk.Advance(time.Hour)
	if r.srv.Count(fake.RouteKey) != n {
		t.Error("a synced key was sent again")
	}
}

func TestKeyPushWaitsForTheOlderCloudThatHasNoKeyEndpoint(t *testing.T) {
	r := newRig(t)
	r.srv.Handshake.Missing = true // an older cloud: no localai routes at all
	r.enroll()
	r.waitAlert(doctor.CLD056, true)
	if r.srv.Count(fake.RouteKey) != 0 {
		t.Error("the key was pushed to a cloud that has no key endpoint")
	}
	for i := 0; i < 6; i++ {
		r.waitTimers(3)
		r.clk.Advance(5 * time.Minute)
	}
	r.waitAlert(doctor.CLD060, true)
	if r.st.Snapshot().Key.CloudSynced {
		t.Error("cloud_synced is true")
	}
	if got := r.link.CloudStatus(); got.KeySynced {
		t.Error("GET /cloud says key_synced")
	}
}

func TestPushAfterRotationSyncsOrReportsTheFailure(t *testing.T) {
	r := newRig(t)
	res := r.enroll()
	r.waitCount(fake.RouteKey, 1)
	waitSynced(t, r, true)

	// A rotation writes a new key; the rotate route calls Push before it reports the result.
	newKey := strings.Repeat("ef56", 16)
	if err := os.WriteFile(r.keyPath, []byte(newKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = r.st.MutateSync(func(s *state.State) error { s.Key.CloudSynced = false; return nil })
	if err := r.link.Push(r.ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.srv.StoredKey(res.ServerNodeID); got != newKey {
		t.Error("the cloud does not hold the new key")
	}
	if !r.st.Snapshot().Key.CloudSynced {
		t.Error("cloud_synced is false after a successful Push")
	}

	// A failing push returns the error and leaves cloud_synced false (the link retries by itself).
	if err := os.WriteFile(r.keyPath, []byte(testKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.srv.Script(fake.RouteKey, fake.Reply{Status: 503})
	err := r.link.Push(r.ctx)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 503 {
		t.Fatalf("Push err = %v", err)
	}
	if r.st.Snapshot().Key.CloudSynced {
		t.Error("cloud_synced stays true after a failed Push of a new key")
	}
	// The key loop picks it up without waiting for the 5 minutes.
	waitSynced(t, r, true)
}

func TestPushWhenNotLinkedSaysSoAndMarksTheKeyUnsynced(t *testing.T) {
	r := newRig(t)
	_ = r.st.MutateSync(func(s *state.State) error { s.Key.CloudSynced = true; return nil })
	if err := r.link.Push(r.ctx); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("err = %v", err)
	}
	if r.st.Snapshot().Key.CloudSynced {
		t.Error("an unlinked node reports cloud_synced true")
	}
	if r.srv.Count(fake.RouteKey) != 0 {
		t.Error("an unlinked node called the cloud")
	}
}

func TestResendKey(t *testing.T) {
	r := newRig(t)
	_, err := r.link.ResendKey(r.ctx)
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != doctor.AGT023 || ae.Status != 409 {
		t.Fatalf("not linked: %v", err)
	}
	r.enroll()
	r.waitCount(fake.RouteKey, 1)
	waitSynced(t, r, true)
	res, err := r.link.ResendKey(r.ctx)
	if err != nil || !res.KeySynced || res.Fingerprint != testKey[:4]+"…"+testKey[60:] {
		t.Fatalf("resend = %+v %v", res, err)
	}
	if r.srv.Count(fake.RouteKey) != 2 {
		t.Errorf("key calls %d", r.srv.Count(fake.RouteKey))
	}
	// A push the cloud does not take answers key_synced false with the fingerprint, not an error.
	r.srv.Script(fake.RouteKey, fake.Reply{Status: 500})
	res, err = r.link.ResendKey(r.ctx)
	if err != nil || res.KeySynced || res.Fingerprint == "" {
		t.Fatalf("failed resend = %+v %v", res, err)
	}
}

func TestKeyIsNeverPushedOverPlainHTTP(t *testing.T) {
	r := newRig(t)
	r.enroll()
	r.waitCount(fake.RouteKey, 1)
	waitSynced(t, r, true)
	n := r.srv.Count(fake.RouteKey)
	// A dev_insecure address: pushKey refuses before any request is made.
	r.link.mu.Lock()
	r.link.st.url = "http://127.0.0.1:9"
	r.link.mu.Unlock()
	if _, err := r.link.pushKey(r.ctx); !errors.Is(err, ErrKeyPushInsecure) {
		t.Fatalf("err = %v", err)
	}
	if r.srv.Count(fake.RouteKey) != n {
		t.Error("a request was made")
	}
}

func TestKeyFileProblemsNeverEchoTheKey(t *testing.T) {
	bad := strings.Repeat("z", 64)
	r := newRig(t, withoutKeyFile())
	for name, content := range map[string]string{"not hex": bad, "short": "ab12", "empty": ""} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(r.keyPath, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := FileKeySource{Path: r.keyPath}.Key()
			if err == nil {
				t.Fatal("a bad key file was accepted")
			}
			if content != "" && strings.Contains(err.Error(), content) {
				t.Errorf("the error echoes the file: %v", err)
			}
		})
	}
	if _, err := (FileKeySource{Path: r.keyPath + ".missing"}).Key(); err == nil {
		t.Error("a missing key file was accepted")
	}
}

func TestKeyReportedFalseInTheHandshakePushesTheKeyAgain(t *testing.T) {
	r := newRig(t)
	r.enroll()
	r.waitCount(fake.RouteKey, 1)
	waitSynced(t, r, true)
	no := false
	r.srv.Handshake.KeyReported = &no
	r.recheckHandshakeNow()
	r.waitCount(fake.RouteKey, 2)
	waitSynced(t, r, true)
}

func TestKeyBodyAndAnswerNeverReachTheLogOnFailure(t *testing.T) {
	r := newRig(t)
	r.srv.Script(fake.RouteKey, fake.Reply{Status: 500, Body: map[string]any{"error": map[string]any{"message": "stored " + testKey, "details": map[string]any{"code": doctor.CLD012}}}})
	r.enroll()
	r.waitCount(fake.RouteKey, 1)
	r.stop()
	if strings.Contains(r.logs.String(), testKey) {
		t.Fatalf("the key reached the log:\n%s", r.logs.String())
	}
}

func TestFingerprint(t *testing.T) {
	if got := Fingerprint(testKey); got != "ab12…cd34" {
		t.Errorf("fingerprint %q", got)
	}
	if Fingerprint("short") != "" {
		t.Error("a short value must not produce a fingerprint")
	}
	raw, _ := json.Marshal(keyBody{Key: "k", Fingerprint: "f", RotatedAt: "t"})
	if string(raw) != `{"key":"k","fingerprint":"f","rotated_at":"t"}` {
		t.Errorf("body shape %s", raw)
	}
}

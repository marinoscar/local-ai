package api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// fakeCloudLink is an api.CloudLink with scripted answers.
type fakeCloudLink struct {
	mu        sync.Mutex
	status    Cloud
	enroll    CloudEnrolled
	enrollErr error
	unenrErr  error
	resend    ResendKeyResult
	resendErr error
	calls     []string
	gotURL    string
	gotCred   string
}

func (f *fakeCloudLink) CloudStatus() Cloud { return f.status }

func (f *fakeCloudLink) Enroll(_ context.Context, url, credential string) (CloudEnrolled, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "enroll")
	f.gotURL, f.gotCred = url, credential
	return f.enroll, f.enrollErr
}

func (f *fakeCloudLink) Unenroll(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "unenroll")
	return f.unenrErr
}

func (f *fakeCloudLink) ResendKey(context.Context) (ResendKeyResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "resend")
	return f.resend, f.resendErr
}

type cloudRig struct {
	t     *testing.T
	link  *fakeCloudLink
	audit *recAudit
	h     http.Handler
}

func newCloudRig(t *testing.T) *cloudRig {
	t.Helper()
	url, name := "https://ai.example.test", "ai-n3"
	id, since, hb, claim, cred := "00000000-0000-4000-8000-000000000001", "2026-11-01T22:10:00Z", "2026-11-02T15:03:55Z", "2026-11-02T15:03:58Z", "nod_…a1b2"
	link := &fakeCloudLink{
		status: Cloud{Enrolled: true, URL: &url, State: "linked", ServerNodeID: &id, Name: &name, Since: &since, LastHeartbeatAt: &hb, LastClaimAt: &claim,
			Credential: &cred, EligibleTypes: []string{"localai.status.refresh"}, ServerFeatures: []string{"node_host_vitals"}, StateTransport: "endpoint", KeySynced: true},
		enroll: CloudEnrolled{State: "linked", ServerNodeID: id},
		resend: ResendKeyResult{KeySynced: true, Fingerprint: "ab12…9f3c"},
	}
	ra := &recAudit{}
	reg := NewRegistry()
	if err := RegisterCloud(reg, CloudDeps{Link: link, Audit: ra}); err != nil {
		t.Fatal(err)
	}
	s, _ := newTestServer(t, func(o *Options) { o.Registry = reg })
	return &cloudRig{t: t, link: link, audit: ra, h: s.Handler()}
}

func TestCloudRoutesAreRegisteredWithTheDocumentedLevels(t *testing.T) {
	reg := NewRegistry()
	if err := RegisterCloud(reg, CloudDeps{Link: &fakeCloudLink{}}); err != nil {
		t.Fatal(err)
	}
	got := map[string]Route{}
	for _, rt := range reg.Routes() {
		got[rt.Method+" "+rt.Pattern] = rt
	}
	want := map[string]Level{"GET /cloud": AuthOp, "DELETE /cloud": AuthAdmin, "POST /cloud/enroll": AuthAdmin, "POST /cloud/resend-key": AuthAdmin}
	if len(got) != len(want) {
		t.Fatalf("routes %v", got)
	}
	for k, lvl := range want {
		if rt, ok := got[k]; !ok || len(rt.Auth) != 1 || rt.Auth[0] != lvl {
			t.Errorf("%s: %+v", k, rt)
		}
	}
	if err := RegisterCloud(NewRegistry(), CloudDeps{}); err == nil {
		t.Error("RegisterCloud without a link must fail")
	}
}

func TestCloudGetShapeAndAuth(t *testing.T) {
	r := newCloudRig(t)
	rec := recordOK(t, do(r.h, req{target: "/api/v1/cloud", header: map[string]string{"X-Test-Principal": "bearer"}}))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	m := decodeMap(t, rec)
	if m["enrolled"] != true || m["state"] != "linked" || m["credential"] != "nod_…a1b2" || m["key_synced"] != true || m["state_transport"] != "endpoint" || m["inflight"] != nil {
		t.Errorf("body %v", m)
	}
	expectError(t, do(r.h, req{target: "/api/v1/cloud"}), 401, CodeUnauthenticated)
	if rec := do(r.h, req{target: "/api/v1/cloud", header: map[string]string{"X-Test-Principal": "portal"}}); rec.Code != 401 && rec.Code != 403 {
		t.Errorf("portal GET /cloud = %d", rec.Code)
	}
	// Not enrolled: the same shape with nulls.
	r.link.status = Cloud{State: "unenrolled", EligibleTypes: []string{}, ServerFeatures: []string{}, StateTransport: "endpoint"}
	rec = recordOK(t, do(r.h, req{target: "/api/v1/cloud", header: map[string]string{"X-Test-Principal": "bearer"}}))
	if m := decodeMap(t, rec); m["enrolled"] != false || m["url"] != nil || m["credential"] != nil {
		t.Errorf("unenrolled body %v", m)
	}
}

func TestCloudAdminRoutesNeedAdmin(t *testing.T) {
	r := newCloudRig(t)
	for _, q := range []req{
		{method: "POST", target: "/api/v1/cloud/enroll", body: `{"url":"https://ai.example.test","credential":"x"}`},
		{method: "DELETE", target: "/api/v1/cloud"},
		{method: "POST", target: "/api/v1/cloud/resend-key", body: `{}`},
	} {
		q.header = jsonHeaders(nil) // an operator, not an admin
		expectError(t, do(r.h, q), 403, CodeAdminRequired)
		q.header = map[string]string{"Content-Type": "application/json"}
		expectError(t, do(r.h, q), 401, CodeUnauthenticated)
	}
	if len(r.link.calls) != 0 {
		t.Errorf("the link was called without admin: %v", r.link.calls)
	}
}

func TestCloudEnrollSuccessAuditsTheMaskedCredentialOnly(t *testing.T) {
	r := newCloudRig(t)
	cred := "nod_" + strings.Repeat("k3Pz", 6)
	body := `{"url":"https://ai.example.test","credential":"` + cred + `"}`
	rec := recordOK(t, do(r.h, req{method: "POST", target: "/api/v1/cloud/enroll", body: body, header: jsonHeaders(map[string]string{"X-Test-Principal": "admin"})}))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if m := decodeMap(t, rec); m["state"] != "linked" || m["server_node_id"] == "" {
		t.Errorf("body %v", m)
	}
	if r.link.gotURL != "https://ai.example.test" || r.link.gotCred != cred {
		t.Errorf("the link got %q %q", r.link.gotURL, r.link.gotCred)
	}
	evs := r.audit.events()
	if len(evs) != 1 || evs[0].Action != audit.ActionCloudEnroll || evs[0].Result != audit.ResultOK || evs[0].Actor.Kind != audit.KindAdmin {
		t.Fatalf("audit %+v", evs)
	}
	for k, v := range evs[0].Target {
		if strings.Contains(v, cred) {
			t.Errorf("audit target %s holds the credential", k)
		}
	}
	if evs[0].Target["credential_fingerprint"] == "" || evs[0].Target["url"] != "https://ai.example.test" || evs[0].Target["state"] != "linked" {
		t.Errorf("audit target %v", evs[0].Target)
	}
}

func TestCloudEnrollBackoffAndStoppedAnswers(t *testing.T) {
	r := newCloudRig(t)
	for name, answer := range map[string]CloudEnrolled{
		"backoff": {State: "backoff", LastError: "cloud: connection refused"},
		"stopped": {State: "stopped"},
	} {
		t.Run(name, func(t *testing.T) {
			r.link.enroll = answer
			body := `{"url":"https://ai.example.test","credential":"nod_` + strings.Repeat("a", 24) + `"}`
			rec := recordOK(t, do(r.h, req{method: "POST", target: "/api/v1/cloud/enroll", body: body, header: jsonHeaders(map[string]string{"X-Test-Principal": "admin"})}))
			if rec.Code != 200 || decodeMap(t, rec)["state"] != name {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestCloudEnrollErrorsKeepTheirCodes(t *testing.T) {
	r := newCloudRig(t)
	for name, tc := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"rejected credential": {NewError(doctor.CLD051, 422, map[string]any{"reason": "credential_rejected"}), 422, doctor.CLD051},
		"not https":           {NewError(doctor.CLD062, 422, nil), 422, doctor.CLD062},
		"busy":                {NewError(CodeBusy, 409, nil), 409, CodeBusy},
		"bug":                 {context.Canceled, 500, CodeInternal},
	} {
		t.Run(name, func(t *testing.T) {
			r.link.enrollErr = tc.err
			r.audit.mu.Lock()
			r.audit.evs = nil
			r.audit.mu.Unlock()
			body := `{"url":"https://ai.example.test","credential":"nod_` + strings.Repeat("a", 24) + `"}`
			expectError(t, do(r.h, req{method: "POST", target: "/api/v1/cloud/enroll", body: body, header: jsonHeaders(map[string]string{"X-Test-Principal": "admin"})}), tc.status, tc.code)
			evs := r.audit.events()
			if len(evs) != 1 || evs[0].Result != audit.ResultFail || evs[0].Code != tc.code {
				t.Errorf("audit %+v", evs)
			}
		})
	}
}

func TestCloudEnrollBodyValidation(t *testing.T) {
	r := newCloudRig(t)
	for name, body := range map[string]string{
		"no url":        `{"credential":"nod_xxxxxxxxxxxx"}`,
		"no credential": `{"url":"https://ai.example.test"}`,
		"blank url":     `{"url":"  ","credential":"nod_xxxxxxxxxxxx"}`,
		"unknown field": `{"url":"https://ai.example.test","credential":"nod_xxxxxxxxxxxx","extra":1}`,
		"not json":      `nope`,
		"wrong type":    `{"url":1,"credential":"x"}`,
		"empty body":    ``,
	} {
		t.Run(name, func(t *testing.T) {
			expectError(t, do(r.h, req{method: "POST", target: "/api/v1/cloud/enroll", body: body, header: jsonHeaders(map[string]string{"X-Test-Principal": "admin"})}), 400, CodeInvalidRequest)
		})
	}
	if len(r.link.calls) != 0 {
		t.Errorf("a refused body reached the link: %v", r.link.calls)
	}
	// A wrong content type is refused before the handler.
	h := jsonHeaders(map[string]string{"X-Test-Principal": "admin", "Content-Type": "text/plain"})
	expectError(t, do(r.h, req{method: "POST", target: "/api/v1/cloud/enroll", body: `{}`, header: h}), 415, CodeInvalidRequest)
}

func TestCloudUnenroll(t *testing.T) {
	r := newCloudRig(t)
	rec := recordOK(t, do(r.h, req{method: "DELETE", target: "/api/v1/cloud", header: jsonHeaders(map[string]string{"X-Test-Principal": "admin"})}))
	if rec.Code != 200 || rec.Body.String() != `{"enrolled":false}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	evs := r.audit.events()
	if len(evs) != 1 || evs[0].Action != audit.ActionCloudUnenroll || evs[0].Result != audit.ResultOK {
		t.Fatalf("audit %+v", evs)
	}
	r.link.unenrErr = NewError(CodeBusy, 409, nil)
	expectError(t, do(r.h, req{method: "DELETE", target: "/api/v1/cloud", header: jsonHeaders(map[string]string{"X-Test-Principal": "admin"})}), 409, CodeBusy)
	if evs := r.audit.events(); len(evs) != 2 || evs[1].Result != audit.ResultFail {
		t.Errorf("audit of the failure %+v", evs)
	}
}

func TestCloudResendKey(t *testing.T) {
	r := newCloudRig(t)
	rec := recordOK(t, do(r.h, req{method: "POST", target: "/api/v1/cloud/resend-key", body: `{}`, header: jsonHeaders(map[string]string{"X-Test-Principal": "admin"})}))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if m := decodeMap(t, rec); m["key_synced"] != true || m["fingerprint"] != "ab12…9f3c" {
		t.Errorf("body %v", m)
	}
	// Not linked: 409 LAI-AGT-023.
	r.link.resendErr = NewError(CodeNotInMode, 409, map[string]any{"reason": "not_linked"})
	expectError(t, do(r.h, req{method: "POST", target: "/api/v1/cloud/resend-key", body: `{}`, header: jsonHeaders(map[string]string{"X-Test-Principal": "admin"})}), 409, CodeNotInMode)
	// The body must be {}: anything else is a 400.
	expectError(t, do(r.h, req{method: "POST", target: "/api/v1/cloud/resend-key", body: `{"force":true}`, header: jsonHeaders(map[string]string{"X-Test-Principal": "admin"})}), 400, CodeInvalidRequest)
}

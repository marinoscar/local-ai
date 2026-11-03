package modelsapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/logx"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// KeyPusher reports the llama key to the cloud (agent.md section 4.8.5); the cloud link client
// (AGENT-17) implements it. Push reads the key and its bookkeeping itself, so the key travels from
// the key file to the HTTPS request without passing through this package, and never holds
// modelOpsMu (RotateKey calls it after the lock is released).
type KeyPusher interface {
	// Push sends the current key. It returns nil when the cloud accepted it, an error that wraps
	// ErrCloudSkipped when there was nothing to do (not enrolled, not linked), and any other error
	// when the push failed (the link retries every five minutes).
	Push(ctx context.Context) error
}

// ErrCloudSkipped is what a KeyPusher wraps when it did not push because the node is not linked to a
// cloud: cloud_synced is false and nothing is logged as a failure.
var ErrCloudSkipped = errors.New("modelsapi: the key was not pushed: the node is not linked to a cloud")

// pushTimeout bounds the push to the cloud after a rotation.
const pushTimeout = 20 * time.Second

// alertSubject is the subject of the LAI-AGT-016 alert a rotation without a restart raises.
const alertSubject = "key"

// clientsToUpdate is what the operator must do after a rotation (agent.md section 3.8.14).
var clientsToUpdate = []string{
	"OpenCode on this computer: ~/.config/opencode/llama-api-key",
	"Any script that uses the old key directly",
}

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Default output limit of a model's OpenCode entry when the catalog has no client_hints.
const defaultMaxOutput = 4096

// ---- GET /key ---------------------------------------------------------------------------------

// ShowKey is GET /key: the only way the full llama key leaves the agent. It writes the key.show audit
// line (with the actor) before it returns the key, and the route marks the response no-store. The
// snippets hold placeholders, never the key. A missing or malformed key file is 500 LAI-SEC-010.
func (s *Service) ShowKey(ctx context.Context, actor audit.Actor) (KeyBody, error) {
	key, err := s.d.Keys.Key()
	if err == nil && !hex64.MatchString(key) {
		err = router.ErrBadKey
	}
	if err != nil {
		reason := "unreadable"
		if errors.Is(err, router.ErrBadKey) {
			reason = "malformed"
		}
		e := api.NewError(doctor.SEC010, http.StatusInternalServerError, map[string]any{"reason": reason})
		s.auditWrite(ctx, audit.ActionKeyShow, audit.ResultFail, e.Code, "", actor, nil)
		return KeyBody{}, e
	}
	fp := router.KeyFingerprint(key)
	s.auditWrite(ctx, audit.ActionKeyShow, audit.ResultOK, "", "", actor, map[string]string{"fingerprint": fp})

	cfg := s.d.Config.Get()
	port := cfg.Ports.Llama
	if port <= 0 {
		port = config.DefaultLlamaPort
	}
	local := "http://127.0.0.1:" + strconv.Itoa(port) + "/v1"
	body := KeyBody{Key: key, Fingerprint: fp, BaseURLs: KeyBaseURLs{Local: local}, Models: []string{}}
	if s.d.Tailnet != nil {
		if u := s.d.Tailnet.TailnetURL(ctx); u != "" {
			body.BaseURLs.Tailnet = &u
		}
	}
	if h := cfg.Node.PublicHost; h != nil && *h != "" {
		u := "https://" + *h + "/v1"
		body.BaseURLs.Public = &u
	}

	// The models a client can ask for: the language models that would load here.
	view := s.d.Models.Models(lang(ctx))
	for _, row := range view.Rows {
		if row.Engine == "llama" && row.Loadable {
			body.Models = append(body.Models, row.ID)
		}
	}
	chosen := "<model>"
	switch {
	case view.Default != "" && containsStr(body.Models, view.Default):
		chosen = view.Default
	case len(body.Models) > 0:
		chosen = body.Models[0]
	}
	body.Snippets = s.snippets(local, chosen, body.Models)
	return body, nil
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// snippets builds the ready-to-paste client configurations for base (the local base URL).
func (s *Service) snippets(base, model string, models []string) KeySnippets {
	cat := s.d.Catalog.Get().Catalog
	oc := OpenCodeProvider{
		NPM:     "@ai-sdk/openai-compatible",
		Options: OpenCodeOptions{BaseURL: base, APIKey: "{file:~/.config/opencode/llama-api-key}"},
		Models:  map[string]OpenCodeModel{},
	}
	for _, id := range models {
		lim := OpenCodeLimit{Output: defaultMaxOutput}
		if cm, ok := cat.Model(id); ok {
			if cm.Llama != nil {
				lim.Context = cm.Llama.Ctx
			}
			if cm.ClientHints != nil && cm.ClientHints.MaxOutputTokens > 0 {
				lim.Output = cm.ClientHints.MaxOutputTokens
			}
		}
		oc.Models[id] = OpenCodeModel{Limit: lim}
	}
	return KeySnippets{
		Curl: "curl -s " + base + `/chat/completions -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d '{"model":"` +
			model + `","messages":[{"role":"user","content":"Hello"}]}'`,
		Env:          "OPENAI_BASE_URL=" + base + "\nOPENAI_API_KEY=<key>",
		OpenCode:     OpenCodeSnippet{Provider: map[string]OpenCodeProvider{"local-llama": oc}},
		OpenAIPython: "from openai import OpenAI\nclient = OpenAI(base_url=\"" + base + "\", api_key=KEY)",
	}
}

func (s *Service) getKey(w http.ResponseWriter, r *http.Request) error {
	body, err := s.ShowKey(r.Context(), api.AuditActor(api.InfoFrom(r.Context())))
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	api.WriteJSON(w, r, http.StatusOK, body)
	return nil
}

// ---- POST /key/rotate -------------------------------------------------------------------------

// KeyRotateRequest is a rotation as the route decoded it.
type KeyRotateRequest struct {
	// Restart restarts llama and restores the models after the new key is written (the default of
	// the route). False leaves llama running with the old key until its next restart and raises
	// LAI-AGT-016.
	Restart bool
	Actor   audit.Actor
}

// keyWriteError is a failure to write the key file; the old key is still in place.
type keyWriteError struct{ err error }

func (e *keyWriteError) Error() string {
	return "modelsapi: the new key could not be written: " + e.err.Error()
}
func (e *keyWriteError) Unwrap() error { return e.err }

// keyWrite is the step of a rotation that runs under modelOpsMu: it writes the new key and records
// it. It holds the key only until the rotation ends.
type keyWrite struct {
	svc *Service
	key string
	fp  string

	written atomic.Bool
	mu      sync.Mutex
	at      time.Time
}

func (w *keyWrite) rotatedAt() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.at
}

// prepare writes the key file atomically (the old key stays whole until the rename), then tells the
// log scrubber and the key source about the new key and records the rotation in state.key. A state
// file that cannot be written is logged and does not undo the rotation: the key is on disk, and the
// next step writes cloud_synced again.
func (w *keyWrite) prepare(_ context.Context, _ string) error {
	s := w.svc
	if err := router.WriteKey(s.d.KeyFS, s.d.KeyPath, w.key); err != nil {
		return &keyWriteError{err: err}
	}
	at := s.now().UTC().Truncate(time.Second)
	w.mu.Lock()
	w.at = at
	w.mu.Unlock()
	w.written.Store(true)

	if s.d.SetLiveKey != nil {
		s.d.SetLiveKey(w.key)
	} else {
		logx.SetCurrentKey(w.key)
	}
	if inv, ok := s.d.Keys.(router.KeyInvalidator); ok {
		inv.Invalidate()
	}
	if s.d.State != nil {
		fp := w.fp
		if err := s.d.State.MutateSync(func(st *state.State) error {
			st.Key.Fingerprint, st.Key.RotatedAt, st.Key.CloudSynced = fp, &at, false
			return nil
		}); err != nil {
			s.log().Error("the rotation could not be recorded in the state file", "fingerprint", fp, "error", err.Error())
		}
	}
	return nil
}

// RotateKey is POST /key/rotate (agent.md section 3.8.14): 32 random bytes as 64 hex digits, written
// atomically to the key file under modelOpsMu; llama restarted and the models restored (unless
// Restart is false); the key pushed to the cloud; state.key updated; key.rotated published; the
// key.rotate audit line written.
//
// A busy lock is 409 LAI-AGT-022 and the old key stays. Once the key is written it stays whatever
// happens next (the old one is assumed leaked): a llama that does not restart is 500 LAI-LLM-001 with
// details.key_rotated true, and so is any other failure after the write (details.fingerprint says
// which key is now the live one).
func (s *Service) RotateKey(ctx context.Context, req KeyRotateRequest) (KeyRotated, error) {
	key, err := router.GenerateKey(s.d.Rand)
	if err != nil {
		s.log().Error("could not generate a key", "error", err.Error())
		e := api.NewError(api.CodeInternal, http.StatusInternalServerError, map[string]any{"reason": "key_generate_failed", "key_rotated": false})
		s.auditWrite(ctx, audit.ActionKeyRotate, audit.ResultFail, e.Code, "", req.Actor, nil)
		return KeyRotated{}, e
	}
	w := &keyWrite{svc: s, key: key, fp: router.KeyFingerprint(key)}
	target := map[string]string{"fingerprint": w.fp, "restart": strconv.FormatBool(req.Restart)}

	var res router.RestartResult
	var opID string
	if req.Restart {
		res, err = s.d.Restarter.RestartWith(ctx, router.RestartRequest{Actor: req.Actor, Prepare: w.prepare})
		opID = res.OpID
	} else {
		opID, err = s.rotateOnly(ctx, w)
	}
	if err != nil {
		return KeyRotated{}, s.rotateFailed(ctx, req, w, opID, target, err)
	}

	synced := s.afterWrite(ctx, w, opID)
	if req.Restart {
		if s.d.Alerts != nil {
			s.d.Alerts.Clear(doctor.AGT016, alertSubject)
		}
	} else if s.d.Alerts != nil {
		// llama still serves the old key, and so does every client of it until the restart.
		s.d.Alerts.Raise(doctor.AGT016, alertSubject, map[string]any{"service": "llama", "reason": "key_rotated"})
	}
	s.auditWrite(ctx, audit.ActionKeyRotate, audit.ResultOK, "", opID, req.Actor, target)

	restored := res.Restored
	if restored == nil || !req.Restart {
		restored = []string{}
	}
	return KeyRotated{
		Fingerprint: w.fp, RotatedAt: w.rotatedAt().Format(time.RFC3339), CloudSynced: synced,
		RestoredModels: append([]string{}, restored...), ClientsToUpdate: append([]string{}, clientsToUpdate...), OpID: opID,
	}, nil
}

// rotateOnly writes the key under modelOpsMu without restarting llama.
func (s *Service) rotateOnly(ctx context.Context, w *keyWrite) (string, error) {
	hold, err := s.d.Lock.TryAcquire(router.HolderRotate)
	if err != nil {
		return "", err
	}
	defer hold.Release()
	return hold.OpID, w.prepare(ctx, hold.OpID)
}

// afterWrite does what follows a written key: push it to the cloud, record cloud_synced, publish
// key.rotated. It returns cloud_synced. The push is skipped outside online mode (there is no
// network), without a pusher, and when the caller has given up.
func (s *Service) afterWrite(ctx context.Context, w *keyWrite, opID string) bool {
	synced := false
	online := s.d.Mode == nil || s.d.Mode.Info().Effective == modes.Online
	if s.d.Pusher != nil && online && ctx.Err() == nil {
		pctx, cancel := context.WithTimeout(ctx, pushTimeout)
		err := s.d.Pusher.Push(pctx)
		cancel()
		switch {
		case err == nil:
			synced = true
		case errors.Is(err, ErrCloudSkipped):
		default:
			s.log().Warn("the new key could not be sent to the cloud; the link retries", "fingerprint", w.fp, "error", err.Error())
		}
	}
	if s.d.State != nil && synced {
		fp := w.fp
		if err := s.d.State.MutateSync(func(st *state.State) error {
			if st.Key.Fingerprint == fp {
				st.Key.CloudSynced = true
			}
			return nil
		}); err != nil {
			s.log().Error("cloud_synced could not be recorded in the state file", "error", err.Error())
		}
	}
	if s.d.Publisher != nil {
		if err := s.d.Publisher.Publish(events.TypeKeyRotated, opID, events.KeyRotated{Fingerprint: w.fp}); err != nil {
			s.log().Warn("key.rotated could not be published", "error", err.Error())
		}
	}
	return synced
}

// rotateFailed turns what went wrong into the answer and the audit line. A failure before the key
// was written leaves the old key; a failure after it keeps the new one and says so.
func (s *Service) rotateFailed(ctx context.Context, req KeyRotateRequest, w *keyWrite, opID string, target map[string]string, err error) error {
	var out error
	var kw *keyWriteError
	var oe *router.OpError
	var busy *router.BusyError
	switch {
	case errors.As(err, &busy):
		// Only the rotation without a restart can see this: the lock was taken before the write.
		out = busyErr(err)
	case errors.As(err, &kw):
		s.log().Error("the new key could not be written; the old key stays", "error", kw.err.Error())
		out = api.NewError(api.CodeInternal, http.StatusInternalServerError, map[string]any{
			"reason": "key_write_failed", "key_rotated": false, "op_id": opID,
		})
	case errors.As(err, &oe) || errors.Is(err, router.ErrInterrupted):
		out = router.APIError(lang(ctx), err)
	default:
		out = err
	}
	if w.written.Load() {
		// The key is on disk; tell the cloud and the event stream, best effort, and say so.
		s.afterWrite(ctx, w, opID)
		out = withDetails(out, map[string]any{"key_rotated": true, "fingerprint": w.fp})
	}
	s.auditWrite(ctx, audit.ActionKeyRotate, audit.ResultFail, codeOf(out), opID, req.Actor, target)
	return out
}

// withDetails returns err with extra details when it is an *api.Error, and err itself otherwise (a
// context error is classified by the server and has no details to add to).
func withDetails(err error, extra map[string]any) error {
	var ae *api.Error
	if !errors.As(err, &ae) {
		return err
	}
	c := *ae
	c.Details = make(map[string]any, len(ae.Details)+len(extra))
	for k, v := range ae.Details {
		c.Details[k] = v
	}
	for k, v := range extra {
		c.Details[k] = v
	}
	return &c
}

// rotateBody is the optional body of POST /key/rotate.
type rotateBody struct {
	Restart *bool `json:"restart"`
}

func (s *Service) rotateKey(w http.ResponseWriter, r *http.Request) error {
	var body rotateBody
	if err := api.DecodeJSONOrEmpty(w, r, &body); err != nil {
		return err
	}
	restart := true
	if body.Restart != nil {
		restart = *body.Restart
	}
	res, err := s.RotateKey(r.Context(), KeyRotateRequest{Restart: restart, Actor: api.AuditActor(api.InfoFrom(r.Context()))})
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) {
			if op, ok := ae.Details["op_id"].(string); ok && op != "" {
				api.SetOpID(w, op)
			}
		}
		return err
	}
	api.SetOpID(w, res.OpID)
	api.WriteJSON(w, r, http.StatusOK, res)
	return nil
}

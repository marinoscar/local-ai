package audit

import (
	"bytes"
	"encoding/json"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/marinoscar/local-ai/agent/internal/logx"
)

// ActorKind says who performed an audited action (agent.md section 4.11).
type ActorKind string

// The actor kinds. CLIRoot is an in-process aictl command run as root, with uid 0 and no agent
// principal.
const (
	KindAnonymous ActorKind = "anonymous"
	KindPortal    ActorKind = "portal"
	KindOperator  ActorKind = "operator"
	KindAdmin     ActorKind = "admin"
	KindCloud     ActorKind = "cloud"
	KindSystem    ActorKind = "system"
	KindCLIRoot   ActorKind = "cli-root"
)

// Via values of Actor.Via. The set is open (the spec ends it with an ellipsis); these are the
// ones the agent uses today.
const (
	ViaBearer  = "bearer"
	ViaSession = "session"
	ViaSocket  = "socket"
	ViaPIN     = "pin"
	ViaPortal  = "portal"
)

// Result of an audited action.
type Result string

// The results. The set is open; a caller may use a more specific word.
const (
	ResultOK     Result = "ok"
	ResultFail   Result = "fail"
	ResultDenied Result = "denied"
)

// The fixed audited actions of agent.md section 4.11. The families agent.update.*, service.* and
// migration.* are built by the caller (for example "service.restart").
const (
	ActionKeyShow          = "key.show"
	ActionKeyRotate        = "key.rotate"
	ActionModelLoad        = "model.load"
	ActionModelUnload      = "model.unload"
	ActionModelDelete      = "model.delete"
	ActionModelSetDefault  = "model.set_default"
	ActionModeChange       = "mode.change"
	ActionPINVerifyOK      = "pin.verify.ok"
	ActionPINVerifyFail    = "pin.verify.fail"
	ActionPINSet           = "pin.set"
	ActionTokenRotate      = "token.rotate"
	ActionUICodeMint       = "ui_code.mint"
	ActionSessionCreate    = "session.create"
	ActionCloudEnroll      = "cloud.enroll"
	ActionCloudUnenroll    = "cloud.unenroll"
	ActionCloudJob         = "cloud.job"
	ActionProfileSet       = "profile.set"
	ActionCatalogRefresh   = "catalog.refresh"
	ActionLlamaBuildSwitch = "llama.build.switch"
	// ActionWhisperBuildSwitch is the switch of a whisper.cpp build (aictl rebuild-whisper).
	ActionWhisperBuildSwitch = "whisper.build.switch"
)

// Actor is the "actor" object of an audit line.
type Actor struct {
	Kind        ActorKind
	Via         string // bearer, session, socket, pin, portal, ...; empty is written as null
	UID         *int   // peer uid when known; nil is written as null
	JobID       string // the cloud job, for KindCloud
	Fingerprint string // the redacted credential used, for example lat_…9xQz
}

// Anonymous is a caller without credentials (survival-mode routes).
func Anonymous() Actor { return Actor{Kind: KindAnonymous} }

// Portal is a Survival Portal session.
func Portal() Actor { return Actor{Kind: KindPortal, Via: ViaPortal} }

// Operator is an operator principal; via is how it authenticated (ViaBearer, ViaSession, ViaSocket).
func Operator(via string) Actor { return Actor{Kind: KindOperator, Via: via} }

// Admin is an administrator principal; via is how it authenticated (ViaSocket, ViaPIN, ...).
func Admin(via string) Actor { return Actor{Kind: KindAdmin, Via: via} }

// Cloud is a command executed on behalf of the cloud app, for the given job.
func Cloud(jobID string) Actor { return Actor{Kind: KindCloud, JobID: jobID} }

// System is the agent itself (timers, startup, the mode engine).
func System() Actor { return Actor{Kind: KindSystem} }

// CLIRoot is an in-process aictl command run as root without the agent: uid 0, no principal.
func CLIRoot() Actor { return Actor{Kind: KindCLIRoot, UID: intp(0)} }

// WithUID returns a copy of a with the peer uid set.
func (a Actor) WithUID(uid int) Actor { a.UID = intp(uid); return a }

// WithJobID returns a copy of a with the job id set.
func (a Actor) WithJobID(id string) Actor { a.JobID = id; return a }

// WithCredential returns a copy of a whose fingerprint is the redacted form of secret
// (logx.Redact). The secret itself is not kept.
func (a Actor) WithCredential(secret string) Actor { a.Fingerprint = logx.Redact(secret); return a }

func intp(n int) *int { return &n }

// Event is one audit record. At is optional (Write stamps the current time).
type Event struct {
	At     time.Time
	Action string
	Result Result
	Code   string // doctor code when the action failed, else empty (written as null)
	Actor  Actor
	Target map[string]string // what the action touched: model, unit, mode, root, ...; empty values are written as null
	OpID   string
	ReqID  string
}

// The wire shape of agent.md section 4.11: every key is always present, absent values are null.
type wireEvent struct {
	At     string             `json:"at"`
	Action string             `json:"action"`
	Result string             `json:"result"`
	Code   *string            `json:"code"`
	Actor  wireActor          `json:"actor"`
	Target map[string]*string `json:"target"`
	OpID   *string            `json:"op_id"`
	ReqID  *string            `json:"req_id"`
}

type wireActor struct {
	Kind        string  `json:"kind"`
	Via         *string `json:"via"`
	UID         *int    `json:"uid"`
	JobID       *string `json:"job_id"`
	Fingerprint *string `json:"fingerprint"`
}

const (
	// maxField caps every free-text field of a line, so a record stays well under MaxLineBytes.
	maxField = 256
	// maxTargetKey caps the length of a target key.
	maxTargetKey = 64
)

// clean makes a field safe to write: scrubbed of secrets (a second line of defence; callers pass
// fingerprints, never secrets) and capped in length. Empty becomes nil (JSON null).
func clean(s string) *string {
	if s == "" {
		return nil
	}
	s = logx.ScrubString(s)
	s = truncate(s, maxField)
	return &s
}

// truncate cuts s to at most n bytes on a rune boundary, marking the cut with an ellipsis.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// wireOf converts an event to its wire shape, scrubbing as it goes. A target value under a
// secret-looking key is redacted, not just scrubbed.
func wireOf(ev Event, at time.Time) wireEvent {
	w := wireEvent{
		At:     at.UTC().Truncate(time.Second).Format(time.RFC3339),
		Action: orUnknown(truncate(logx.ScrubString(ev.Action), maxField)),
		Result: orUnknown(truncate(logx.ScrubString(string(ev.Result)), maxField)),
		Code:   clean(ev.Code),
		Actor: wireActor{
			Kind:        orUnknown(string(ev.Actor.Kind)),
			Via:         clean(ev.Actor.Via),
			UID:         ev.Actor.UID,
			JobID:       clean(ev.Actor.JobID),
			Fingerprint: clean(ev.Actor.Fingerprint),
		},
		Target: make(map[string]*string, len(ev.Target)),
		OpID:   clean(ev.OpID),
		ReqID:  clean(ev.ReqID),
	}
	for k, v := range ev.Target {
		k = truncate(logx.ScrubString(k), maxTargetKey)
		if v != "" && logx.SensitiveKey(k) {
			r := logx.Redact(v)
			w.Target[k] = &r
			continue
		}
		w.Target[k] = clean(v)
	}
	return w
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// encode renders one line (with its trailing newline) of at most MaxLineBytes. A record that
// would be longer loses its target, then keeps only the essentials; a line is never cut in the
// middle, because one write(2) of a whole line is what keeps appends from two processes from
// interleaving.
func encode(w wireEvent) []byte {
	line := marshal(w)
	if len(line) <= MaxLineBytes {
		return line
	}
	note := "target dropped: record longer than the line limit"
	w.Target = map[string]*string{"truncated": &note}
	if line = marshal(w); len(line) <= MaxLineBytes {
		return line
	}
	return marshal(wireEvent{At: w.At, Action: w.Action, Result: w.Result, Actor: wireActor{Kind: w.Actor.Kind}, Target: map[string]*string{"truncated": &note}})
}

func marshal(w wireEvent) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // keep <tailnet-ip> readable
	if err := enc.Encode(w); err != nil {
		// Cannot happen for this fixed shape of strings, ints and nulls; never lose the record.
		return []byte(`{"at":"` + w.At + `","action":"unknown","result":"unknown","code":null,"actor":{"kind":"unknown"},"target":{},"op_id":null,"req_id":null}` + "\n")
	}
	return buf.Bytes()
}

// sortedKeys returns the keys of m in order, for stable slog output.
func sortedKeys(m map[string]*string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

const (
	oldModel = "/home/labuser/models/m1/Model-One-Q4.gguf"
	newModel = "/srv/localai/models/model-one/Model-One-Q4.gguf"
)

// assertMigrated checks everything a finished migration leaves behind.
func assertMigrated(t *testing.T, w *world, where string) {
	t.Helper()
	f := func(format string, a ...any) { t.Helper(); t.Fatalf(where+": "+format, a...) }

	// The old unit is retired and masked; its text is in the backup.
	st, _ := w.sd.State(context.Background(), LegacyUnitName)
	if st.UnitFileState != "masked" || st.ActiveState == "active" {
		f("legacy unit: %+v", st)
	}
	if got, ok := w.fs.content("/var/lib/localai/migration/llama-qwen/llama-qwen.service"); !ok || string(got) != w.legacyText {
		f("the backup of the legacy unit is missing or differs")
	}
	// The new unit is enabled and serves.
	ns, _ := w.sd.State(context.Background(), NewUnitName)
	if !ns.Enabled() || !ns.Active() {
		f("new unit: %+v", ns)
	}
	// The key: same bytes at the new path (root 0600), and the old path is a link to it.
	if got := mustRead(t, w, "/etc/localai/api-key"); got != w.key {
		f("the new key differs from the old one")
	}
	if fi, _ := w.fs.Stat("/etc/localai/api-key"); fi.Mode().Perm() != 0o600 {
		f("the new key mode is %v", fi.Mode().Perm())
	}
	if tgt, err := w.fs.Readlink("/etc/llama-qwen/api-key"); err != nil || tgt != "/etc/localai/api-key" {
		f("the old key path: %q %v", tgt, err)
	}
	// The model: at the new place, owned by localai, with a manifest; the old path is a link.
	if got, ok := w.fs.content(newModel); !ok || string(got) != string(w.model) {
		f("the model is not at %s", newModel)
	}
	if tgt, err := w.fs.Readlink(oldModel); err != nil || tgt != newModel {
		f("the old model path: %q %v", tgt, err)
	}
	w.fs.mu.Lock()
	n := w.fs.nodes[newModel]
	w.fs.mu.Unlock()
	if n.uid != 990 || n.gid != 990 || n.mode != 0o644 {
		f("the model owner/mode: %d:%d %o", n.uid, n.gid, n.mode)
	}
	m, err := manifest.Read(w.fs, "/srv/localai/models/model-one/MANIFEST.json")
	if err != nil || m.ItemID != "model-one" || m.Source.Type != "adopted" || m.Trust != manifest.TrustPinned || len(m.Files) == 0 {
		f("the manifest: %+v %v", m, err)
	}
	// Configuration and state.
	if got := w.cfgNode.cfg.Llama.LegacyAliases["model-one"]; len(got) != 1 || got[0] != "legacy-name" {
		f("legacy_aliases = %v", got)
	}
	if w.st.def != "model-one" {
		f("state.models.default = %q", w.st.def)
	}
	if !w.st.has || w.st.entry.State != StateDone {
		f("state.migration = %+v", w.st.entry)
	}
	// The journal.
	var pr Progress
	if err := json.Unmarshal([]byte(mustRead(t, w, "/var/lib/localai/migration/llama-qwen/progress.json")), &pr); err != nil || pr.Step != M10 || pr.T0 == nil || pr.TLoaded == nil {
		f("progress.json: %+v %v", pr, err)
	}
	var res Result
	if err := json.Unmarshal([]byte(mustRead(t, w, "/var/lib/localai/migration/llama-qwen/result.json")), &res); err != nil || res.State != StateDone || len(res.ClientsToUpdate) != 0 || !res.ServeUnchanged {
		f("result.json: %+v %v", res, err)
	}
	// The port and the URL did not change: the new unit listens where the old one did.
	if w.portOwner() != "new" {
		f("port 8080 is held by %q", w.portOwner())
	}
}

func TestMigrationOnTheSameFilesystem(t *testing.T) {
	w := newWorld(t, worldOpts{hasCurrent: true})
	res, err := w.run(w.engine())
	if err != nil {
		t.Fatalf("%v\n%s", err, w.out.String())
	}
	if res.State != StateDone || res.ModelID != "model-one" || res.Alias != "legacy-name" {
		t.Fatalf("result %+v", res)
	}
	assertMigrated(t, w, "same fs")
	// Downtime is the time between the old service stopping and the model being loaded: a few polls.
	if res.DowntimeSeconds <= 0 || res.DowntimeSeconds > 60 {
		t.Fatalf("downtime = %v", res.DowntimeSeconds)
	}
	// The single writer rule: the agent was stopped and started again, once each.
	if w.agent.stops != 1 || w.agent.starts != 1 || !w.agent.active {
		t.Fatalf("agent stops=%d starts=%d active=%v", w.agent.stops, w.agent.starts, w.agent.active)
	}
	// No build was needed: current already had the commit.
	if len(w.builder.built) != 0 {
		t.Fatalf("built %v", w.builder.built)
	}
	// The model file was renamed, never re-read after M0: same inode, no .part, no second copy.
	if w.fs.has(newModel + ".part") {
		t.Fatal("a .part is left")
	}
	// The report names what the migration promised.
	out := w.out.String()
	for _, want := range []string{"Migration done.", "clients to update: none", "tailscale serve: unchanged", "legacy-name"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report lacks %q:\n%s", want, out)
		}
	}
	// The audit trail.
	joined := strings.Join(w.audits, "\n")
	if !strings.Contains(joined, "migration.llama_qwen.done ok") || !strings.Contains(joined, "migration.llama_qwen.start ok") {
		t.Fatalf("audit: %v", w.audits)
	}
}

func TestMigrationAcrossFilesystemsCopiesAndKeepsTheOriginalUntilM9(t *testing.T) {
	w := newWorld(t, worldOpts{hasCurrent: true, crossFS: true})
	e := w.engine()
	p, err := e.Prepare(context.Background())
	if err != nil || !p.OK() {
		t.Fatalf("%v %+v", err, p.Problems)
	}
	// Watch the original: it must still be a real file when the old service is stopped (M6).
	sawOriginal := false
	e2 := w.engine(func(o *Options) {
		o.Systemd = &hookSD{fakeSD: w.sd, before: func(verb, unit string) {
			if verb == "stop" && unit == LegacyUnitName {
				fi, err := w.fs.Lstat(oldModel)
				sawOriginal = err == nil && fi.Mode().IsRegular()
			}
		}}
	})
	if _, err := e2.Run(context.Background(), p); err != nil {
		t.Fatalf("%v\n%s", err, w.out.String())
	}
	if !sawOriginal {
		t.Fatal("the original file was gone before the old service was stopped: the copy case must keep it until M9")
	}
	assertMigrated(t, w, "cross fs")
	if w.fs.has(newModel + ".part") {
		t.Fatal("a .part is left")
	}
	// A rename across two devices is impossible (EXDEV), so the content really was copied.
	if err := w.fs.Rename("/home/x", "/srv/x"); err == nil {
		t.Fatal("expected a failure")
	}
}

// hookSD calls before for every verb, then the fake.
type hookSD struct {
	*fakeSD
	before func(verb, unit string)
}

func (h *hookSD) Stop(ctx context.Context, unit string) error {
	h.before("stop", unit)
	return h.fakeSD.Stop(ctx, unit)
}

func TestMigrationWithDraftFileAndDropInEnvironment(t *testing.T) {
	w := newWorld(t, worldOpts{hasCurrent: true, withDraft: true, dropIn: true})
	res, err := w.run(w.engine())
	if err != nil {
		t.Fatalf("%v\n%s", err, w.out.String())
	}
	if res.State != StateDone {
		t.Fatalf("%+v", res)
	}
	// Both files moved, both left links, the manifest lists both.
	for _, name := range []string{"Model-One-Q4.gguf", "draft.gguf"} {
		if tgt, err := w.fs.Readlink("/home/labuser/models/m1/" + name); err != nil || tgt != "/srv/localai/models/model-one/"+name {
			t.Errorf("%s: %q %v", name, tgt, err)
		}
	}
	m, err := manifest.Read(w.fs, "/srv/localai/models/model-one/MANIFEST.json")
	if err != nil || len(m.Files) != 2 {
		t.Fatalf("manifest %+v %v", m, err)
	}
	// The GGML variable of the drop-in is carried to node.yaml; the drop-in is in the backup, not in /etc.
	if got := w.cfgNode.cfg.Llama.ExtraEnv["GGML_VK_SUBALLOCATION_BLOCK_SIZE"]; got != "4294967296" {
		t.Errorf("extra_env = %v", w.cfgNode.cfg.Llama.ExtraEnv)
	}
	if w.fs.has("/etc/systemd/system/llama-qwen.service.d/vk.conf") || w.fs.has("/etc/systemd/system/llama-qwen.service.d") {
		t.Error("the drop-in is still in /etc/systemd/system")
	}
	if got, ok := w.fs.content("/var/lib/localai/migration/llama-qwen/llama-qwen.service.d/vk.conf"); !ok || !strings.Contains(string(got), "GGML_VK") {
		t.Error("the drop-in is not in the backup")
	}
}

func TestMaskWorksOnlyBecauseTheUnitFileWasMovedOut(t *testing.T) {
	w := newWorld(t, worldOpts{hasCurrent: true})
	if _, err := w.run(w.engine()); err != nil {
		t.Fatal(err)
	}
	// The fake systemd refuses to mask while the file is in /etc/systemd/system ("File exists"): the
	// unit file must have been removed first, and the mask is the /dev/null link now there.
	if tgt, err := w.fs.Readlink("/etc/systemd/system/llama-qwen.service"); err != nil || tgt != "/dev/null" {
		t.Fatalf("the mask link: %q %v", tgt, err)
	}
	order := strings.Join(w.sd.calls, ",")
	if strings.Index(order, "disable llama-qwen.service") > strings.Index(order, "mask llama-qwen.service") {
		t.Fatalf("the unit is masked before it is disabled: %s", order)
	}
	// And the contrary: a node where the file could not be removed fails loudly at M9, with the old
	// service stopped for good only after a verified new one (here: the new one already serves).
	w2 := newWorld(t, worldOpts{hasCurrent: true})
	w2.fs.k = w2.k
	w2.sd.failAlways["mask llama-qwen.service"] = errors.New("File exists")
	_, err := w2.run(w2.engine())
	if err == nil || CodeOf(err) != CodeInterrupted {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(w2.out.String(), "File exists") && !strings.Contains(err.Error(), "File exists") {
		t.Fatalf("the failure does not say why: %v", err)
	}
	// The new service keeps serving (the migration is verified), and a re-run finishes it.
	if w2.portOwner() != "new" {
		t.Fatalf("the port owner is %q", w2.portOwner())
	}
	delete(w2.sd.failAlways, "mask llama-qwen.service")
	if _, err := w2.run(w2.engine()); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	assertMigrated(t, w2, "after a failed mask")
}

func TestTheKeyIsNeverPrintedOrLogged(t *testing.T) {
	w := newWorld(t, worldOpts{hasCurrent: true, withDraft: true})
	var logs strings.Builder
	e := w.engine(func(o *Options) { o.Logger = newLogger(&logs) })
	if _, err := w.run(e); err != nil {
		t.Fatal(err)
	}
	// Also a failing run and a rollback: the key must not leak through error text either.
	w2 := newWorld(t, worldOpts{hasCurrent: true})
	w2.chatStatus = 500
	var logs2 strings.Builder
	_, err := w2.run(w2.engine(func(o *Options) { o.Logger = newLogger(&logs2) }))
	if err == nil {
		t.Fatal("expected a rollback")
	}
	for name, text := range map[string]string{
		"output": w.out.String(), "logs": logs.String(), "audit": strings.Join(w.audits, "\n"),
		"rollback output": w2.out.String(), "rollback logs": logs2.String(), "rollback error": err.Error(), "rollback audit": strings.Join(w2.audits, "\n"),
	} {
		if strings.Contains(text, w.key) || strings.Contains(text, w2.key) {
			t.Errorf("%s contains the key", name)
		}
	}
	// Neither does any file of the migration directory: only the fingerprint.
	for _, dir := range []*world{w, w2} {
		dir.fs.mu.Lock()
		for p, n := range dir.fs.nodes {
			if strings.HasPrefix(p, "/var/lib/localai/migration/") && strings.Contains(string(n.data), dir.key) {
				t.Errorf("%s contains the key", p)
			}
		}
		dir.fs.mu.Unlock()
	}
	// The plan carries the fingerprint only.
	if plan := mustRead(t, w, "/var/lib/localai/migration/llama-qwen/plan.json"); !strings.Contains(plan, "key_fingerprint") || strings.Contains(plan, w.key) {
		t.Errorf("plan.json: %s", plan)
	}
}

func TestPortAndPublicURLDoNotChange(t *testing.T) {
	w := newWorld(t, worldOpts{hasCurrent: true})
	var asked []string
	w.cfgNode.cfg.Node.PublicHost = ptr("node.example.test")
	w.st.conn = "full"
	e := w.engine()
	if _, err := w.run(e); err != nil {
		t.Fatalf("%v\n%s", err, w.out.String())
	}
	// The Serve configuration was read before and after and is the same; nothing ever wrote it.
	for _, c := range w.execCalls {
		if strings.HasPrefix(c, "tailscale ") {
			asked = append(asked, c)
			if !strings.Contains(c, "serve status") {
				t.Errorf("tailscale was asked to do something else: %s", c)
			}
		}
	}
	if len(asked) < 2 {
		t.Fatalf("tailscale asked %d times", len(asked))
	}
	if before := mustRead(t, w, "/var/lib/localai/migration/llama-qwen/serve-before.json"); before != w.serveJSON {
		t.Errorf("serve-before.json = %s", before)
	}
	// The unit the migration generated listens where the old one did: the plan says 127.0.0.1:8080.
	var p Plan
	if err := json.Unmarshal([]byte(mustRead(t, w, "/var/lib/localai/migration/llama-qwen/plan.json")), &p); err != nil || p.Port != 8080 || p.Host != "127.0.0.1" {
		t.Fatalf("plan %+v %v", p, err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestResultIsJSONWithoutClientChanges(t *testing.T) {
	w := newWorld(t, worldOpts{hasCurrent: true})
	res, err := w.run(w.engine())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), `"clients_to_update":[]`) {
		t.Fatalf("%s", b)
	}
}

func TestSecondRunAfterDoneRepairsOnlyTheKeyLink(t *testing.T) {
	w := newWorld(t, worldOpts{hasCurrent: true})
	if _, err := w.run(w.engine()); err != nil {
		t.Fatal(err)
	}
	snap := w.fs.snapshot()
	calls := len(w.sd.calls)
	res, err := w.run(w.engine())
	if err != nil || res.State != "already_done" {
		t.Fatalf("%+v %v", res, err)
	}
	if w.fs.snapshot() != snap || len(w.sd.calls) != calls {
		t.Fatal("a second run changed something")
	}
	// Somebody put a regular file back at the old key path (LAI-SEC-015): the run makes it a link again.
	w.fs.put("/etc/llama-qwen/api-key", []byte(w.key), 0o600)
	res, err = w.run(w.engine())
	if err != nil || res.State != "already_done" {
		t.Fatalf("%+v %v", res, err)
	}
	if tgt, err := w.fs.Readlink("/etc/llama-qwen/api-key"); err != nil || tgt != "/etc/localai/api-key" {
		t.Fatalf("the key path: %q %v", tgt, err)
	}
	if !strings.Contains(w.out.String(), "link to /etc/localai/api-key again") {
		t.Fatalf("output: %s", w.out.String())
	}
}

func TestNothingRealWaits(t *testing.T) {
	w := newWorld(t, worldOpts{hasCurrent: true})
	w.loadPolls = 25 // the model takes 25 polls to load
	if _, err := w.run(w.engine()); err != nil {
		t.Fatal(err)
	}
	if w.clk.slept < 25 {
		t.Fatalf("slept %d times", w.clk.slept)
	}
}

func TestDetailsOfTheExitCodes(t *testing.T) {
	// CodeOf finds a code in a wrapped error and says nothing for a plain one.
	e := wrap(CodeStopped, M0, os.ErrNotExist, "x")
	if CodeOf(e) != CodeStopped || CodeOf(os.ErrNotExist) != "" {
		t.Fatal("CodeOf")
	}
	if got := problemsError([]Problem{{Code: CodeStopped, Cause: "a", Message: "m"}, {Code: CodeBadKey, Message: "n"}}); got.Code != CodeStopped || len(got.Details["also"].([]string)) != 1 {
		t.Fatalf("%+v", got)
	}
	if problemsError(nil) != nil {
		t.Fatal("no problems, no error")
	}
}

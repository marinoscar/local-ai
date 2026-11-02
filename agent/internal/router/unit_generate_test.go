package router

import (
	"errors"
	"math/rand"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/services"
	servicesfake "github.com/marinoscar/local-ai/agent/internal/services/fake"
)

func mustGolden(t *testing.T, w *unitWorld, name string) {
	t.Helper()
	checkGolden(t, name, []byte(w.unitText()))
}

func hasFinding(res UnitResult, code Code, subject string) bool {
	for _, f := range res.Findings {
		if f.Code == code && (subject == "*" || f.Subject == subject) {
			return true
		}
	}
	return false
}

func TestUnitGenerateFirstRun(t *testing.T) {
	w := newUnitWorld(t, func(o *UnitGeneratorOptions) {
		o.CacheOwner = Ownership{UID: 990, GID: 990}
	})
	res := w.must(routerIn(1, 512))
	mustGolden(t, w, "localai-llama.router.service")
	if !res.Written || !res.Reloaded || res.RestartRequired || res.Mode != LlamaModeRouter {
		t.Fatalf("result: %+v", res)
	}
	if res.SHA256 != SHA256Hex([]byte(w.unitText())) || w.st.sha != res.SHA256 {
		t.Fatal("the hash of the unit as written is recorded in state")
	}
	if e := w.fs.entry(unitPath); e.mode != 0o644 {
		t.Fatalf("unit mode %v, want 0644", e.mode)
	}
	if w.sys.n != 1 {
		t.Fatalf("daemon-reload ran %d times, want 1", w.sys.n)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("findings on a clean first run: %+v", res.Findings)
	}
	// The cache directory: created, localai 0750, empty.
	c := w.fs.entry(cachePath)
	if c == nil || !c.dir || c.mode != 0o750 || c.uid != 990 || c.gid != 990 {
		t.Fatalf("cache dir: %+v", c)
	}
	if !res.Cache.Created || res.Cache.Entries != 0 || res.Cache.Path != cachePath {
		t.Fatalf("cache status: %+v", res.Cache)
	}
	if len(w.alerts.events) != 0 {
		t.Fatalf("alerts: %+v", w.alerts.events)
	}
}

func TestUnitGenerateGoldens(t *testing.T) {
	t.Run("router ai-n1", func(t *testing.T) {
		w := newUnitWorld(t)
		w.must(routerIn(3, 2048))
		mustGolden(t, w, "localai-llama.router.ai-n1.service")
	})
	t.Run("single ai-n3", func(t *testing.T) {
		w := newUnitWorld(t)
		res := w.must(singleIn(w.singleOnDisk()))
		mustGolden(t, w, "localai-llama.single.service")
		if res.Mode != LlamaModeSingle || !hasFinding(res, CodeSingleMode, "") {
			t.Fatalf("single mode is announced with an info finding: %+v", res)
		}
		if !w.alerts.has("raise", CodeSingleMode, "") {
			t.Fatal("LAI-LLM-023 was not raised")
		}
	})
}

// TestUnitGenerateIdempotent: a byte-identical render does not rewrite the file and does not
// daemon-reload.
func TestUnitGenerateIdempotent(t *testing.T) {
	w := newUnitWorld(t)
	w.must(routerIn(1, 512))
	before := w.fs.entry(unitPath)
	for i := 0; i < 3; i++ {
		res := w.must(routerIn(1, 512))
		if res.Written || res.Reloaded || res.RestartRequired || len(res.Findings) != 0 {
			t.Fatalf("run %d: %+v", i, res)
		}
	}
	if w.sys.n != 1 {
		t.Fatalf("daemon-reload ran %d times for one change", w.sys.n)
	}
	if w.fs.entry(unitPath) != before {
		t.Fatal("an identical render replaced the file")
	}
	if w.fs.has(unitPath + ".prev") {
		t.Fatal("no .prev without a replacement")
	}
	// A new generator over the same disk and state (an agent restart) is just as quiet.
	w2 := newUnitWorld(t)
	w2.fs, w2.st = w.fs, w.st
	g, err := NewUnitGenerator(UnitGeneratorOptions{Paths: DefaultUnitPaths(""), FS: w.fs, MkdirAll: w.fs.MkdirAll, State: w.st, Systemd: w2.sys, Clock: w.clock})
	if err != nil {
		t.Fatal(err)
	}
	if res, err := g.Generate(t.Context(), routerIn(1, 512)); err != nil || res.Written || w2.sys.n != 0 {
		t.Fatalf("after a restart: %+v %v reloads=%d", res, err, w2.sys.n)
	}
}

// TestUnitGenerateOneReloadPerChange: with the services fake, a change gives exactly one
// DaemonReload and no restart, and nothing else touches a unit.
func TestUnitGenerateOneReloadPerChange(t *testing.T) {
	mgr := servicesfake.New()
	w := newUnitWorld(t, func(o *UnitGeneratorOptions) { o.Systemd = mgr })
	w.must(routerIn(1, 512))
	w.must(routerIn(1, 512))
	calls := mgr.Calls()
	if len(calls) != 1 || calls[0].Verb != services.VerbDaemonReload {
		t.Fatalf("calls after the first write: %+v", calls)
	}
	mgr.Reset()
	res := w.must(routerIn(2, 512))
	calls = mgr.Calls()
	if len(calls) != 1 || calls[0].Verb != services.VerbDaemonReload {
		t.Fatalf("calls after a change: %+v", calls)
	}
	if !res.Reloaded {
		t.Fatal("Reloaded is false")
	}
	for _, c := range calls {
		if c.Verb == services.VerbRestart || c.Verb == services.VerbStart || c.Verb == services.VerbStop {
			t.Fatalf("the generator never restarts llama: %+v", c)
		}
	}
}

func TestUnitGenerateChangeNeedsRestart(t *testing.T) {
	w := newUnitWorld(t)
	w.must(routerIn(1, 512))
	old := w.unitText()
	res := w.must(routerIn(3, 2048))
	if !res.Written || !res.RestartRequired || strings.Join(res.Changed, ",") != "ExecStart" {
		t.Fatalf("result: %+v", res)
	}
	if !w.alerts.has("raise", CodeUnitRestart, "llama-unit") {
		t.Fatal("LAI-AGT-016 was not raised")
	}
	if b, _ := w.fs.get(unitPath + ".prev"); string(b) != old {
		t.Fatal(".prev must hold the previous unit")
	}
	if w.sys.n != 2 {
		t.Fatalf("reloads: %d", w.sys.n)
	}
	// Back to the old inputs: another change, another restart need.
	res = w.must(routerIn(1, 512))
	if !res.RestartRequired || w.unitText() != old {
		t.Fatalf("back to the old unit: %+v", res)
	}
}

func TestUnitGenerateModeSwitch(t *testing.T) {
	w := newUnitWorld(t)
	w.must(routerIn(1, 512))
	res := w.must(singleIn(w.singleOnDisk()))
	if !res.Written || !res.RestartRequired || !res.Reloaded {
		t.Fatalf("router to single: %+v", res)
	}
	if !w.alerts.has("raise", CodeSingleMode, "") {
		t.Fatal("LAI-LLM-023 not raised in single mode")
	}
	res = w.must(routerIn(1, 512))
	if !res.RestartRequired || !res.Written {
		t.Fatalf("single to router: %+v", res)
	}
	if !w.alerts.has("clear", CodeSingleMode, "") {
		t.Fatal("LAI-LLM-023 not cleared when router mode is back")
	}
	mustGolden(t, w, "localai-llama.router.service")
}

// TestUnitGenerateHandEdit: a file that is not what was last written is saved aside and replaced.
func TestUnitGenerateHandEdit(t *testing.T) {
	w := newUnitWorld(t)
	w.must(routerIn(1, 512))
	edited := strings.Replace(w.unitText(), "--port 8080", "--port 9999", 1)
	w.fs.put(unitPath, []byte(edited), 0o644)
	w.alerts.reset()
	w.clock.advance(time.Hour)
	res := w.must(routerIn(1, 512))
	want := unitPath + ".hand-edited-20261102T160405Z"
	if res.HandEditedCopy != want {
		t.Fatalf("saved as %q, want %q", res.HandEditedCopy, want)
	}
	if b, _ := w.fs.get(want); string(b) != edited {
		t.Fatal("the saved copy is not the edited file")
	}
	mustGolden(t, w, "localai-llama.router.service")
	if !w.alerts.has("raise", CodeUnitHandEdited, "unit") || !hasFinding(res, CodeUnitHandEdited, "unit") {
		t.Fatalf("LAI-LLM-006 not raised: %+v", w.alerts.events)
	}
	if !res.Written || !res.RestartRequired || !res.Reloaded {
		t.Fatalf("result: %+v", res)
	}
	// A second edit in the same second keeps the first copy.
	w.fs.put(unitPath, []byte(edited+"# again\n"), 0o644)
	res = w.must(routerIn(1, 512))
	if res.HandEditedCopy != want+"-2" {
		t.Fatalf("second copy: %q", res.HandEditedCopy)
	}
}

func TestUnitGenerateHandEditIsNotReportedTwice(t *testing.T) {
	w := newUnitWorld(t)
	w.must(routerIn(1, 512))
	w.fs.put(unitPath, []byte("# someone's own\n[Service]\nExecStart=/bin/true\n"), 0o644)
	w.must(routerIn(1, 512))
	w.alerts.reset()
	res := w.must(routerIn(1, 512))
	if res.HandEditedCopy != "" || len(w.alerts.events) != 0 || res.Written {
		t.Fatalf("the regenerated file is ours again: %+v %+v", res, w.alerts.events)
	}
}

func TestUnitGenerateWithoutARecordedHash(t *testing.T) {
	t.Run("a file with our header is ours", func(t *testing.T) {
		w := newUnitWorld(t)
		w.fs.put(unitPath, []byte(UnitHeader+"[Service]\nExecStart=/bin/old\n"), 0o644)
		res := w.must(routerIn(1, 512))
		if res.HandEditedCopy != "" || !res.Written || w.alerts.has("raise", CodeUnitHandEdited, "*") {
			t.Fatalf("result: %+v", res)
		}
		if !res.RestartRequired {
			t.Fatal("the old unit ran another command line")
		}
	})
	t.Run("a file without it is foreign and kept", func(t *testing.T) {
		w := newUnitWorld(t)
		foreign := "[Service]\nExecStart=/bin/old\n"
		w.fs.put(unitPath, []byte(foreign), 0o644)
		res := w.must(routerIn(1, 512))
		if res.HandEditedCopy == "" || !w.alerts.has("raise", CodeUnitHandEdited, "unit") {
			t.Fatalf("result: %+v", res)
		}
		if b, _ := w.fs.get(res.HandEditedCopy); string(b) != foreign {
			t.Fatal("the foreign file was not kept")
		}
	})
	t.Run("a file that already holds the new unit loses nothing and needs no reload", func(t *testing.T) {
		// A crash between the rename and the state update.
		w := newUnitWorld(t)
		w.must(routerIn(1, 512))
		w.st.sha = ""
		w.alerts.reset()
		res := w.must(routerIn(1, 512))
		if res.Written || res.HandEditedCopy != "" || len(w.alerts.events) != 0 || w.sys.n != 1 {
			t.Fatalf("result: %+v alerts %+v reloads %d", res, w.alerts.events, w.sys.n)
		}
		if w.st.sha != res.SHA256 || res.SHA256 == "" {
			t.Fatal("the hash must be recorded again")
		}
	})
}

// ---- failure paths ----

func TestUnitGenerateWriteFailures(t *testing.T) {
	cases := []struct {
		name string
		arm  func(w *unitWorld)
	}{
		{"read-only file system on open", func(w *unitWorld) {
			w.fs.failOp["open"] = &os.PathError{Op: "open", Path: unitPath, Err: syscall.EROFS}
		}},
		{"no space left on write", func(w *unitWorld) { w.fs.diskFull = 10 }},
		{"rename refused", func(w *unitWorld) {
			w.fs.failOp["rename"] = &os.PathError{Op: "rename", Path: unitPath, Err: syscall.EROFS}
		}},
		{"fsync fails", func(w *unitWorld) { w.fs.failOp["sync"] = syscall.EIO }},
		{"write fails", func(w *unitWorld) { w.fs.failOp["write"] = syscall.ENOSPC }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newUnitWorld(t)
			w.must(routerIn(1, 512))
			good := w.unitText()
			recorded := w.st.sha
			reloads := w.sys.n
			w.alerts.reset()
			c.arm(w)
			res, err := w.generate(routerIn(3, 2048))
			if err == nil {
				t.Fatal("a failed write must be an error")
			}
			if w.unitText() != good || w.st.sha != recorded {
				t.Fatal("the old unit and the recorded hash must be untouched")
			}
			if res.Written || res.Reloaded || w.sys.n != reloads {
				t.Fatalf("nothing is reloaded after a failed write: %+v", res)
			}
			if !hasFinding(res, CodeUnitFailed, "write unit") || !w.alerts.has("raise", CodeUnitFailed, "write unit") {
				t.Fatalf("LAI-LLM-007 not raised: %+v", res.Findings)
			}
			if names := w.fs.names("/etc/systemd/system"); len(names) != 1 && !(len(names) == 2 && names[0] == "localai-llama.service" && strings.HasSuffix(names[1], ".prev")) {
				t.Fatalf("a temp file was left behind: %v", names)
			}
			// The disk recovers: the next run writes and clears the alert.
			w.fs.failOp = map[string]error{}
			w.fs.diskFull = -1
			if res := w.must(routerIn(3, 2048)); !res.Written || !res.RestartRequired {
				t.Fatalf("recovery: %+v", res)
			}
			if !w.alerts.has("clear", CodeUnitFailed, "write unit") {
				t.Fatal("the alert is not cleared after recovery")
			}
		})
	}
}

func TestUnitGenerateDirectorySyncFailureCountsAsWritten(t *testing.T) {
	w := newUnitWorld(t)
	w.fs.failOp["syncdir"] = syscall.EIO
	res, err := w.generate(routerIn(1, 512))
	if err != nil || !res.Written || !res.Reloaded {
		t.Fatalf("%+v %v", res, err)
	}
	mustGolden(t, w, "localai-llama.router.service")
}

func TestUnitGenerateUnwritableDirectory(t *testing.T) {
	// The unit's directory is missing: nothing can be created next to it.
	w := newUnitWorld(t)
	delete(w.fs.entries, "/etc/systemd/system")
	res, err := w.generate(routerIn(1, 512))
	if err == nil || res.Written || w.sys.n != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if !hasFinding(res, CodeUnitFailed, "write unit") {
		t.Fatalf("findings: %+v", res.Findings)
	}
}

func TestUnitGenerateReadFailure(t *testing.T) {
	w := newUnitWorld(t)
	w.must(routerIn(1, 512))
	w.fs.failPath[unitPath] = syscall.EACCES
	res, err := w.generate(routerIn(3, 2048))
	if err == nil || res.Written || !hasFinding(res, CodeUnitFailed, "read unit") {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestUnitGenerateStateFailureIsRecoverable(t *testing.T) {
	w := newUnitWorld(t)
	w.st.err = errors.New("disk full")
	res := w.must(routerIn(1, 512))
	if !res.Written || w.st.sha != "" {
		t.Fatalf("%+v", res)
	}
	w.st.err = nil
	w.alerts.reset()
	res = w.must(routerIn(1, 512))
	if res.Written || res.HandEditedCopy != "" || w.st.sha != res.SHA256 || len(w.alerts.events) != 0 {
		t.Fatalf("the next run recognises its own file: %+v", res)
	}
}

func TestUnitGenerateSaveAsideFailureKeepsTheFile(t *testing.T) {
	w := newUnitWorld(t)
	w.must(routerIn(1, 512))
	edited := w.unitText() + "# local change\n"
	w.fs.put(unitPath, []byte(edited), 0o644)
	w.fs.diskFull = 5 // the copy cannot be written
	res, err := w.generate(routerIn(1, 512))
	if err == nil || res.Written {
		t.Fatalf("%+v %v", res, err)
	}
	if w.unitText() != edited {
		t.Fatal("a file that could not be kept must not be overwritten")
	}
	if !hasFinding(res, CodeUnitFailed, "save the hand-edited unit") {
		t.Fatalf("findings: %+v", res.Findings)
	}
}

func TestUnitGenerateBadBinary(t *testing.T) {
	w := newUnitWorld(t)
	w.must(routerIn(1, 512))
	good := w.unitText()
	for _, bad := range []string{"relative/llama-server", "/opt/with space/llama-server", "/opt/x;y/llama-server", "/opt/%h/llama-server"} {
		in := routerIn(3, 2048)
		in.Binary = bad
		res, err := w.generate(in)
		var ue *UnitError
		if !errors.As(err, &ue) || ue.Subject != "binary" {
			t.Fatalf("%q: %v", bad, err)
		}
		if w.unitText() != good || res.Written {
			t.Fatalf("%q: a unit that cannot work replaced the good one", bad)
		}
		if !hasFinding(res, CodeUnitFailed, "unit") {
			t.Fatalf("%q: findings %+v", bad, res.Findings)
		}
	}
}

func TestUnitGenerateBinaryMissing(t *testing.T) {
	cases := []struct {
		name   string
		arm    func(w *unitWorld)
		reason string
	}{
		{"missing", func(w *unitWorld) { delete(w.fs.entries, DefaultLlamaBinary) }, AccessMissing},
		{"not executable", func(w *unitWorld) { w.fs.entry(DefaultLlamaBinary).mode = 0o644 }, "not_executable"},
		{"a directory", func(w *unitWorld) { w.fs.entry(DefaultLlamaBinary).dir = true }, AccessNotFile},
		{"stat fails", func(w *unitWorld) { w.fs.failPath[DefaultLlamaBinary] = syscall.EIO }, AccessError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newUnitWorld(t)
			c.arm(w)
			res, err := w.generate(routerIn(1, 512))
			if err != nil || !res.Written {
				t.Fatalf("the unit is written anyway: %+v %v", res, err)
			}
			var found *PresetFinding
			for i := range res.Findings {
				if res.Findings[i].Code == CodeBinaryMissing {
					found = &res.Findings[i]
				}
			}
			if found == nil || found.Details["reason"] != c.reason || found.Details["path"] != DefaultLlamaBinary {
				t.Fatalf("LAI-LLM-002: %+v", res.Findings)
			}
			if !w.alerts.has("raise", CodeBinaryMissing, "binary") {
				t.Fatal("not raised")
			}
		})
	}
	t.Run("cleared when the build lands", func(t *testing.T) {
		w := newUnitWorld(t)
		delete(w.fs.entries, DefaultLlamaBinary)
		w.must(routerIn(1, 512))
		w.fs.put(DefaultLlamaBinary, []byte("elf"), 0o755)
		w.must(routerIn(1, 512))
		if !w.alerts.has("clear", CodeBinaryMissing, "binary") {
			t.Fatal("not cleared")
		}
	})
}

func TestUnitGenerateReloadFailure(t *testing.T) {
	w := newUnitWorld(t)
	w.sys.err = errors.New("bus down")
	res := w.must(routerIn(1, 512))
	if !res.Written || res.Reloaded || res.ReloadErr == nil || !hasFinding(res, CodeUnitFailed, "daemon-reload") {
		t.Fatalf("%+v", res)
	}
	// The file is unchanged now, but systemd has not read it: the next run reloads again.
	w.sys.err = nil
	res = w.must(routerIn(1, 512))
	if res.Written || !res.Reloaded || res.ReloadErr != nil || w.sys.n != 2 {
		t.Fatalf("retry: %+v reloads=%d", res, w.sys.n)
	}
	if !w.alerts.has("clear", CodeUnitFailed, "daemon-reload") {
		t.Fatal("not cleared")
	}
	res = w.must(routerIn(1, 512))
	if res.Reloaded || w.sys.n != 2 {
		t.Fatalf("settled: %+v reloads=%d", res, w.sys.n)
	}
}

func TestUnitGenerateRefusedInputsKeepTheUnit(t *testing.T) {
	w := newUnitWorld(t)
	w.must(routerIn(1, 512))
	good := w.unitText()
	noLlama := singleModelWith()
	noLlama.Model.Llama = nil
	for name, in := range map[string]UnitInputs{
		"unknown mode":           {Mode: "cluster"},
		"single without a model": {Mode: LlamaModeSingle},
		"port out of range":      {Port: 99999},
		"single, unsafe path":    singleIn(&SingleModel{Model: singleN3().Model, Root: "/media/a;b"}),
		"single, no llama block": singleIn(noLlama),
	} {
		res, err := w.generate(in)
		if err == nil || res.Written || w.unitText() != good {
			t.Errorf("%s: %+v %v", name, res, err)
		}
		if !hasFinding(res, CodeUnitFailed, "unit") {
			t.Errorf("%s: findings %+v", name, res.Findings)
		}
	}
	if w.sys.n != 1 {
		t.Fatalf("a refused input reloads nothing: %d", w.sys.n)
	}
}

// TestUnitGenerateSingleModelMustBeReadable: the unit that works is never replaced by one whose
// model the unit's user cannot read.
func TestUnitGenerateSingleModelMustBeReadable(t *testing.T) {
	w := newUnitWorld(t)
	w.must(routerIn(1, 512))
	good := w.unitText()
	sm := singleN3() // its files are not on the fake disk
	res, err := w.generate(singleIn(sm))
	if err == nil || res.Written || w.unitText() != good {
		t.Fatalf("%+v %v", res, err)
	}
	if !hasFinding(res, CodeFilesMissing, sm.Model.ID) || !w.alerts.has("raise", CodeFilesMissing, sm.Model.ID) {
		t.Fatalf("LAI-LLM-008: %+v", res.Findings)
	}
	// With the files in place it goes through.
	installModel(w.fs.memFS, rootInternal, *sm.Model)
	if res := w.must(singleIn(sm)); !res.Written {
		t.Fatalf("%+v", res)
	}
}

func TestUnitGenerateDroppedAliases(t *testing.T) {
	w := newUnitWorld(t)
	sm := w.singleOnDisk()
	sm.Aliases = []string{"qwen3.5-9b", "Bad Alias"}
	res := w.must(singleIn(sm))
	if strings.Join(res.DroppedAliases, ",") != "Bad Alias" || !hasFinding(res, CodeUnitFailed, "alias:Bad Alias") {
		t.Fatalf("%+v", res)
	}
	mustGolden(t, w, "localai-llama.single.service")
	// Fixing the node.yaml clears it.
	sm.Aliases = []string{"qwen3.5-9b"}
	w.must(singleIn(sm))
	if !w.alerts.has("clear", CodeUnitFailed, "alias:Bad Alias") {
		t.Fatal("not cleared")
	}
}

// ---- the cache directory ----

func TestUnitGenerateCacheDirectory(t *testing.T) {
	t.Run("an existing empty directory is left as it is", func(t *testing.T) {
		w := newUnitWorld(t)
		_ = w.fs.MkdirAll(cachePath, 0o750)
		res := w.must(routerIn(1, 512))
		if res.Cache.Created || res.Cache.Entries != 0 || len(w.fs.chmods) != 0 || len(w.fs.chowns) != 0 {
			t.Fatalf("%+v chmods=%v chowns=%v", res.Cache, w.fs.chmods, w.fs.chowns)
		}
	})
	t.Run("wrong mode and owner are corrected", func(t *testing.T) {
		w := newUnitWorld(t, func(o *UnitGeneratorOptions) { o.CacheOwner = Ownership{UID: 990, GID: 991} })
		_ = w.fs.MkdirAll(cachePath, 0o755)
		w.must(routerIn(1, 512))
		c := w.fs.entry(cachePath)
		if c.mode != 0o750 || c.uid != 990 || c.gid != 991 {
			t.Fatalf("%+v", c)
		}
	})
	t.Run("a directory that is not empty raises LAI-LLM-024 and nothing is deleted", func(t *testing.T) {
		w := newUnitWorld(t)
		_ = w.fs.MkdirAll(cachePath, 0o750)
		for _, n := range []string{"a.gguf", "b.gguf", "c.gguf", "d.gguf", "e.gguf", "f.gguf", "g.gguf"} {
			w.fs.put(cachePath+"/"+n, []byte("x"), 0o644)
		}
		res := w.must(routerIn(1, 512))
		if res.Cache.Entries != 7 || !hasFinding(res, CodeCacheModels, "llama-cache") || !w.alerts.has("raise", CodeCacheModels, "llama-cache") {
			t.Fatalf("%+v %+v", res.Cache, res.Findings)
		}
		for _, f := range res.Findings {
			if f.Code == CodeCacheModels {
				names := f.Details["names"].([]string)
				if len(names) != 5 || names[0] != "a.gguf" || f.Details["entries"] != 7 {
					t.Fatalf("details: %+v", f.Details)
				}
			}
		}
		if len(w.fs.names(cachePath)) != 7 {
			t.Fatal("the cache directory was emptied: user data must never be deleted silently")
		}
		if !res.Written {
			t.Fatal("the unit is written regardless")
		}
		// The operator empties it: the alert clears.
		for _, n := range w.fs.names(cachePath) {
			delete(w.fs.entries, cachePath+"/"+n)
		}
		w.must(routerIn(1, 512))
		if !w.alerts.has("clear", CodeCacheModels, "llama-cache") {
			t.Fatal("not cleared")
		}
	})
	t.Run("a file in its place", func(t *testing.T) {
		w := newUnitWorld(t)
		w.fs.put(cachePath, []byte("x"), 0o644)
		res := w.must(routerIn(1, 512))
		if !hasFinding(res, CodeUnitFailed, "llama-cache") || !res.Written {
			t.Fatalf("%+v", res)
		}
		if b, _ := w.fs.get(cachePath); string(b) != "x" {
			t.Fatal("the file was touched")
		}
	})
	steps := map[string]func(w *unitWorld){
		"create": func(w *unitWorld) { w.fs.failOp["mkdir"] = syscall.EROFS },
		"chmod": func(w *unitWorld) {
			_ = w.fs.MkdirAll(cachePath, 0o755)
			w.fs.failOp["chmod"] = syscall.EPERM
		},
		"chown": func(w *unitWorld) {
			_ = w.fs.MkdirAll(cachePath, 0o750)
			w.fs.failOp["chown"] = syscall.EPERM
		},
		"read": func(w *unitWorld) {
			_ = w.fs.MkdirAll(cachePath, 0o750)
			w.fs.failDir[cachePath] = syscall.EIO
		},
		"stat": func(w *unitWorld) { w.fs.failPath[cachePath] = syscall.EIO },
	}
	for step, arm := range steps {
		t.Run("failure: "+step, func(t *testing.T) {
			w := newUnitWorld(t, func(o *UnitGeneratorOptions) { o.CacheOwner = Ownership{UID: 990, GID: 990} })
			arm(w)
			res := w.must(routerIn(1, 512))
			var f *PresetFinding
			for i := range res.Findings {
				if res.Findings[i].Code == CodeUnitFailed && res.Findings[i].Subject == "llama-cache" {
					f = &res.Findings[i]
				}
			}
			if f == nil || f.Details["step"] != step || !res.Written {
				t.Fatalf("step %s: %+v written=%v", step, res.Findings, res.Written)
			}
		})
	}
	t.Run("no cache path, no cache work", func(t *testing.T) {
		w := newUnitWorld(t, func(o *UnitGeneratorOptions) { o.Paths.CacheDir = "" })
		res := w.must(routerIn(1, 512))
		if res.Cache.Path != "" || w.fs.entry(cachePath) != nil {
			t.Fatalf("%+v", res.Cache)
		}
	})
}

// ---- drop-in goldens, by backend ----

// TestDropInGoldenAndBackends: the unit is the same for every backend (the backend is what was
// built, not what is configured); what differs is the environment drop-in, which section 3.3
// writes only when there is an environment.
func TestDropInGoldenAndBackends(t *testing.T) {
	t.Run("vulkan node with a node-level variable", func(t *testing.T) {
		w := aiN1World(t)
		w.llama.ExtraEnv = map[string]string{"GGML_VK_SUBALLOCATION_BLOCK_SIZE": "4294967296"}
		w.build()
		res := w.mustGenerate()
		b, ok := w.fs.get(dropInPath)
		if !ok || !res.DropInChanged || !res.RestartNeeded {
			t.Fatalf("drop-in: %+v", res)
		}
		checkGolden(t, "50-env.conf", b)
		if e := w.fs.entries[dropInPath]; e.mode != 0o644 {
			t.Fatalf("drop-in mode %v", e.mode)
		}
		if !w.alerts.has("raise", CodePresetRestart, "llama-env") {
			t.Fatal("LAI-AGT-016 not raised")
		}
	})
	t.Run("cuda and cpu nodes have none", func(t *testing.T) {
		for _, node := range []*world{newWorld(t), func() *world {
			w := newWorld(t)
			w.fit = NewEnv(snapCPUOnly(), "cpu", routerBlock(1, 0), 0)
			return w
		}()} {
			node.build()
			res := node.mustGenerate()
			if node.fs.has(dropInPath) || res.DropInChanged {
				t.Fatalf("no environment, no drop-in: %+v", res)
			}
		}
	})
	t.Run("the unit does not depend on the backend", func(t *testing.T) {
		a := mustRender(t, UnitInputs{Router: routerBlock(1, 512)})
		for _, backend := range []string{"cuda", "vulkan", "cpu", "rocm"} {
			_ = backend // RenderUnit takes no backend: the same bytes by construction
			if mustRender(t, UnitInputs{Router: routerBlock(1, 512)}) != a {
				t.Fatalf("backend %s", backend)
			}
		}
		for _, line := range strings.Split(a, "\n") {
			for _, word := range []string{"cuda", "vulkan", "rocm", "CUDA", "GGML_"} {
				if strings.Contains(line, word) {
					t.Fatalf("a backend name in the unit: %q", line)
				}
			}
		}
	})
	t.Run("the drop-in goes away when the environment does", func(t *testing.T) {
		w := aiN1World(t)
		w.llama.ExtraEnv = map[string]string{"GGML_VK_SUBALLOCATION_BLOCK_SIZE": "4294967296"}
		w.build()
		w.mustGenerate()
		w.llama.ExtraEnv = nil
		res := w.mustGenerate()
		if w.fs.has(dropInPath) || !res.DropInChanged || !res.RestartNeeded {
			t.Fatalf("%+v", res)
		}
	})
}

// ---- properties ----

// TestUnitGenerateConverges: for any sequence of inputs the unit on disk is the rendering of the
// last one, the reload count is the number of times the bytes changed, and a repeat is silent.
func TestUnitGenerateConverges(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	w := newUnitWorld(t)
	sm := w.singleOnDisk()
	var last string
	changes := 0
	for i := 0; i < 200; i++ {
		var in UnitInputs
		switch rng.Intn(3) {
		case 0:
			in = routerIn(1+rng.Intn(3), []int{0, 512, 2048}[rng.Intn(3)])
		case 1:
			in = singleIn(sm)
		default:
			in = routerIn(2, 512)
		}
		in.Port = []int{0, 8080, 18080}[rng.Intn(3)]
		want, err := RenderUnit(in)
		if err != nil {
			t.Fatal(err)
		}
		res := w.must(in)
		if string(want.Data) != last {
			changes++
			if !res.Written || !res.Reloaded {
				t.Fatalf("step %d: a change must write and reload: %+v", i, res)
			}
		} else if res.Written || res.Reloaded {
			t.Fatalf("step %d: no change must be silent: %+v", i, res)
		}
		last = string(want.Data)
		if w.unitText() != last {
			t.Fatalf("step %d: the disk does not hold the rendering", i)
		}
		if w.st.sha != SHA256Hex(want.Data) {
			t.Fatalf("step %d: the recorded hash is stale", i)
		}
		if again := w.must(in); again.Written || again.Reloaded {
			t.Fatalf("step %d: a repeat is not silent: %+v", i, again)
		}
	}
	if w.sys.n != changes {
		t.Fatalf("reloads %d, changes %d", w.sys.n, changes)
	}
}

func TestNewUnitGeneratorRequirements(t *testing.T) {
	if _, err := NewUnitGenerator(UnitGeneratorOptions{Paths: DefaultUnitPaths("")}); err == nil {
		t.Fatal("a state is required")
	}
	if _, err := NewUnitGenerator(UnitGeneratorOptions{State: &memUnit{}}); err == nil {
		t.Fatal("the unit path is required")
	}
	if p := DefaultUnitPaths("/tmp/root"); p.Unit != "/tmp/root/etc/systemd/system/localai-llama.service" || p.CacheDir != "/tmp/root/var/lib/localai/llama-cache" {
		t.Fatalf("%+v", p)
	}
}

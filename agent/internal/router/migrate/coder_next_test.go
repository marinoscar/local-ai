package migrate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/services"
)

// coderNextWorld is ai-n1: the model is served by llama-coder-next.service, and llama-qwen.service is
// installed beside it, stopped and disabled (llama-router.md section 4.9.1).
func coderNextWorld(t *testing.T, o worldOpts) *world {
	t.Helper()
	w := newWorld(t, o)
	w.legacyUnit = services.UnitLlamaCoderNext
	qwen := mustRead(t, w, "/etc/systemd/system/llama-qwen.service")
	w.legacyText = strings.Replace(w.legacyText, "llama-qwen.service", w.legacyUnit, 1)
	w.fs.put("/etc/systemd/system/"+w.legacyUnit, []byte(w.legacyText), 0o644)
	w.fs.put("/etc/systemd/system/llama-qwen.service", []byte(qwen), 0o644)
	w.sd.units[w.legacyUnit] = &unitRec{active: "active", mainPID: 500, enabled: true}
	w.sd.units[LegacyUnitName] = &unitRec{active: "inactive"}
	return w
}

func TestPreflightChoosesTheLegacyUnitThatServes(t *testing.T) {
	w := coderNextWorld(t, worldOpts{hasCurrent: true})
	p, err := w.engine().Preflight(context.Background())
	if err != nil || !p.OK() {
		t.Fatalf("%v %+v", err, p.Problems)
	}
	if p.LegacyUnit != services.UnitLlamaCoderNext {
		t.Fatalf("legacy unit = %q", p.LegacyUnit)
	}
	if len(p.OtherUnits) != 1 || p.OtherUnits[0] != (OtherUnit{Unit: LegacyUnitName, Enabled: false}) {
		t.Fatalf("other units = %+v", p.OtherUnits)
	}
}

func TestPreflightPrefersAnEnabledUnitWhenNoneRuns(t *testing.T) {
	w := coderNextWorld(t, worldOpts{hasCurrent: true})
	w.sd.units[w.legacyUnit].active, w.sd.units[w.legacyUnit].mainPID = "inactive", 0
	p, err := w.engine().Preflight(context.Background())
	if err != nil || p.LegacyUnit != services.UnitLlamaCoderNext {
		t.Fatalf("%v legacy unit = %q", err, p.LegacyUnit)
	}
	// Neither runs nor is enabled: the usual unit.
	w.sd.units[w.legacyUnit].enabled = false
	if p, _ = w.engine().Preflight(context.Background()); p.LegacyUnit != LegacyUnitName {
		t.Fatalf("legacy unit = %q", p.LegacyUnit)
	}
}

func TestMigrateFromLlamaCoderNextMasksBothUnits(t *testing.T) {
	w := coderNextWorld(t, worldOpts{hasCurrent: true, dropIn: true})
	res, err := w.run(w.engine())
	if err != nil || res.State != StateDone {
		t.Fatalf("%+v %v\n%s", res, err, w.out.String())
	}
	// Only the unit that served was stopped; both are retired, masked and saved.
	if w.sd.called("stop", services.UnitLlamaCoderNext) != 1 || w.sd.called("stop", LegacyUnitName) != 0 {
		t.Fatalf("calls: %v", w.sd.calls)
	}
	for _, u := range LegacyUnitNames {
		st, _ := w.sd.State(context.Background(), u)
		if st.UnitFileState != "masked" || st.Active() {
			t.Fatalf("%s: %+v", u, st)
		}
		if _, ok := w.fs.content("/var/lib/localai/migration/llama-qwen/" + u); !ok {
			t.Fatalf("the backup of %s is missing", u)
		}
	}
	if got, _ := w.fs.content("/var/lib/localai/migration/llama-qwen/llama-coder-next.service"); string(got) != w.legacyText {
		t.Fatal("the backup of llama-coder-next.service differs")
	}
	var p Plan
	if err := json.Unmarshal([]byte(mustRead(t, w, "/var/lib/localai/migration/llama-qwen/plan.json")), &p); err != nil || p.LegacyUnit != services.UnitLlamaCoderNext {
		t.Fatalf("plan.json: %+v %v", p, err)
	}
	if ns, _ := w.sd.State(context.Background(), NewUnitName); !ns.Enabled() || !ns.Active() {
		t.Fatalf("new unit: %+v", ns)
	}
	// The doctor finds no old unit enabled or running.
	if got := runCheck(t, w, CheckLegacyService); len(got) != 0 {
		t.Fatalf("doctor: %+v", got)
	}
}

func TestRollbackFromLlamaCoderNextRestoresBothUnitsAsTheyWere(t *testing.T) {
	w := coderNextWorld(t, worldOpts{hasCurrent: true})
	if res, err := w.run(w.engine()); err != nil || res.State != StateDone {
		t.Fatalf("%+v %v\n%s", res, err, w.out.String())
	}
	res, err := w.engine().Rollback(context.Background())
	if err != nil || res.State != StateRolledBack {
		t.Fatalf("%+v %v\n%s", res, err, w.out.String())
	}
	// The unit that served is back, enabled and running; the other one is back, stopped and disabled.
	cn, _ := w.sd.State(context.Background(), services.UnitLlamaCoderNext)
	if cn.UnitFileState != "enabled" || !cn.Active() {
		t.Fatalf("llama-coder-next: %+v", cn)
	}
	qw, _ := w.sd.State(context.Background(), LegacyUnitName)
	if qw.UnitFileState != "disabled" || qw.Active() {
		t.Fatalf("llama-qwen: %+v", qw)
	}
	if got := mustRead(t, w, "/etc/systemd/system/llama-coder-next.service"); got != w.legacyText {
		t.Fatal("llama-coder-next.service was not restored")
	}
	if w.sd.called("start", LegacyUnitName) != 0 {
		t.Fatalf("llama-qwen was started: %v", w.sd.calls)
	}
	if !strings.Contains(w.out.String(), "Rolled back: llama-coder-next.service serves again") {
		t.Fatalf("output:\n%s", w.out.String())
	}
}

func TestLegacyServiceCheckReportsEveryKnownUnit(t *testing.T) {
	w := coderNextWorld(t, worldOpts{})
	w.sd.units[LegacyUnitName].enabled = true
	got := runCheck(t, w, CheckLegacyService)
	if len(got) != 2 || got[0].Details["unit"] != LegacyUnitName || got[1].Details["unit"] != services.UnitLlamaCoderNext {
		t.Fatalf("%+v", got)
	}
}

// runCheck runs one doctor check of the migration over the world.
func runCheck(t *testing.T, w *world, id string) []doctor.Result {
	t.Helper()
	for _, c := range Checks(CheckSources{Systemd: w.sd, State: snapState{w}, Paths: DefaultPaths(""), Lstat: w.fs.Lstat, Readlink: w.fs.Readlink}) {
		if c.ID == id {
			return c.Run(context.Background())
		}
	}
	t.Fatalf("no check %s", id)
	return nil
}

func TestMigrateFindsTheShardsOfACatalogNameWithAFolder(t *testing.T) {
	// ai-n1 keeps the shards flat in one folder; the catalog names them with the repo's subfolder.
	w := newWorld(t, worldOpts{hasCurrent: true})
	shard2 := []byte("shard two weights")
	w.fs.put("/home/labuser/models/m1/Model-One-Q4-00002-of-00002.gguf", shard2, 0o644)
	m := w.cat.Models[0]
	m.Files = []catalog.ModelFile{
		{Kind: "weights", Name: "Sub/" + w.fileName, SizeBytes: int64(len(w.model)), SHA256: sha256Hex(w.model)},
		{Kind: "weights", Name: "Sub/Model-One-Q4-00002-of-00002.gguf", SizeBytes: int64(len(shard2)), SHA256: sha256Hex(shard2)},
	}
	w.cat = catalog.New(w.cat.Version, []catalog.Model{m}, nil, nil)
	res, err := w.run(w.engine())
	if err != nil || res.State != StateDone {
		t.Fatalf("%+v %v\n%s", res, err, w.out.String())
	}
	for name, want := range map[string][]byte{w.fileName: w.model, "Model-One-Q4-00002-of-00002.gguf": shard2} {
		if got, ok := w.fs.content("/srv/localai/models/model-one/Sub/" + name); !ok || string(got) != string(want) {
			t.Fatalf("%s is not in the model's folder", name)
		}
		if tgt, err := w.fs.Readlink("/home/labuser/models/m1/" + name); err != nil || tgt != "/srv/localai/models/model-one/Sub/"+name {
			t.Fatalf("the old path of %s: %q %v", name, tgt, err)
		}
	}
}

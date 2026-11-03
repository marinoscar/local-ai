package router

import (
	"errors"
	"testing"
)

func TestUnitBackupAndRestore(t *testing.T) {
	w := newUnitWorld(t)
	sm := w.singleOnDisk()
	w.must(singleIn(sm))
	before := w.unitText()
	sha := w.st.UnitSHA256()

	b, err := w.gen.Backup(bg)
	if err != nil || !b.Exists || string(b.Data) != before || b.Recorded != sha {
		t.Fatalf("Backup = %+v, %v", b, err)
	}
	if p, ok := w.fs.get(unitPath + ".prev"); !ok || string(p) != before {
		t.Fatalf(".prev is not a copy of the unit: %q", p)
	}

	// Another model is written, then the backup is restored byte for byte, with the hash and a reload.
	w.must(routerIn(1, 512))
	if w.unitText() == before {
		t.Fatal("the unit did not change")
	}
	reloads := w.sys.n
	if err := w.gen.Restore(bg, b); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if w.unitText() != before || w.st.UnitSHA256() != sha {
		t.Fatalf("the unit or its hash was not restored")
	}
	if w.sys.n != reloads+1 {
		t.Fatalf("daemon-reload ran %d times, want 1", w.sys.n-reloads)
	}
	// And the next Generate sees nothing hand-edited.
	res := w.must(singleIn(sm))
	if res.HandEditedCopy != "" {
		t.Fatalf("a restored unit was taken for a hand edit: %+v", res)
	}
}

func TestUnitBackupOfNoUnit(t *testing.T) {
	w := newUnitWorld(t)
	b, err := w.gen.Backup(bg)
	if err != nil || b.Exists {
		t.Fatalf("Backup = %+v, %v", b, err)
	}
	w.must(routerIn(1, 512))
	if err := w.gen.Restore(bg, b); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, ok := w.fs.get(unitPath); ok {
		t.Fatal("the unit that did not exist before is still there")
	}
}

func TestUnitRestoreFailures(t *testing.T) {
	w := newUnitWorld(t)
	w.must(routerIn(1, 512))
	b, _ := w.gen.Backup(bg)
	w.must(singleIn(w.singleOnDisk()))

	w.sys.err = errors.New("no systemd")
	if err := w.gen.Restore(bg, b); err == nil {
		t.Fatal("a failed daemon-reload was not reported")
	}
	w.sys.err = nil
	w.fs.memFS.mu.Lock()
	w.fs.memFS.failOp["rename"] = errors.New("read-only file system")
	w.fs.memFS.mu.Unlock()
	if err := w.gen.Restore(bg, b); err == nil {
		t.Fatal("a failed write was not reported")
	}
}

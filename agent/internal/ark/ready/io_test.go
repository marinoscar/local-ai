package ready_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark/ready"
	"github.com/marinoscar/local-ai/agent/internal/config"
	cfgfake "github.com/marinoscar/local-ai/agent/internal/config/fake"
)

func TestWriteAndRead(t *testing.T) {
	dir := t.TempDir()
	rep := goldenScenario("fresh-internal")
	data, err := ready.Write(config.OSFS{}, dir, rep)
	if err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(filepath.Join(dir, ready.FileName))
	if err != nil || string(onDisk) != string(data) {
		t.Fatalf("the returned bytes are the file's: %v", err)
	}
	if string(data) != string(readGolden(t, "fresh-internal.json")) {
		t.Error("the file is the golden report")
	}
	got, raw, err := ready.Read(config.OSFS{}, dir)
	if err != nil || string(raw) != string(data) {
		t.Fatalf("Read: %v", err)
	}
	if got.Overall != rep.Overall || got.Root.ID != rep.Root.ID || len(got.Items) != len(rep.Items) || got.Counts != rep.Counts {
		t.Errorf("round trip lost something: %+v", got.Counts)
	}
	again, _ := got.MarshalIndented()
	if string(again) != string(data) {
		t.Error("parse then marshal is not the identity: a reader that rewrites would change the file")
	}
	if ready.Path(dir) != filepath.Join(dir, "READY.json") {
		t.Errorf("path = %s", ready.Path(dir))
	}
}

func TestWriteIsAtomic(t *testing.T) {
	for _, op := range []string{"open", "write", "sync", "chmod", "close", "rename"} {
		t.Run(op, func(t *testing.T) {
			dir := t.TempDir()
			good := goldenScenario("fresh-internal")
			if _, err := ready.Write(config.OSFS{}, dir, good); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(dir, ready.FileName))
			faulty := cfgfake.New(config.OSFS{}).FailOn(op, -1)
			if _, err := ready.Write(faulty, dir, goldenScenario("unclean-drive")); err == nil {
				t.Fatal("the failure must be returned")
			}
			after, _ := os.ReadFile(filepath.Join(dir, ready.FileName))
			if string(after) != string(before) {
				t.Error("the previous report must be intact after a failed write")
			}
			if ents, _ := os.ReadDir(dir); len(ents) != 1 {
				t.Errorf("a temp file was left: %v", ents)
			}
		})
	}
	t.Run("directory sync", func(t *testing.T) {
		dir := t.TempDir()
		faulty := cfgfake.New(config.OSFS{}).FailOn("syncdir", -1)
		_, err := ready.Write(faulty, dir, goldenScenario("fresh-internal"))
		if !ready.IsDurabilityOnly(err) {
			t.Fatalf("err = %v, want a durability-only error", err)
		}
		if _, _, err := ready.Read(config.OSFS{}, dir); err != nil {
			t.Errorf("the new file is in place: %v", err)
		}
	})
	if ready.IsDurabilityOnly(errors.New("x")) || ready.IsDurabilityOnly(nil) {
		t.Error("IsDurabilityOnly")
	}
}

func TestReadErrors(t *testing.T) {
	good := string(readGolden(t, "fresh-internal.json"))
	tests := []struct {
		name    string
		content string
		missing bool
		want    error
	}{
		{name: "missing file", missing: true},
		{name: "empty file", content: "", want: ready.ErrCorrupt},
		{name: "truncated", content: good[:len(good)/2], want: ready.ErrCorrupt},
		{name: "not JSON", content: "overall: green", want: ready.ErrCorrupt},
		{name: "a list", content: "[]", want: ready.ErrCorrupt},
		{name: "a newer schema", content: strings.Replace(good, `"schema_version": 1`, `"schema_version": 2`, 1), want: ready.ErrSchema},
		{name: "no schema", content: strings.Replace(good, `"schema_version": 1,`, ``, 1), want: ready.ErrSchema},
		{name: "a colour that is not one", content: strings.Replace(good, `"overall": "green"`, `"overall": "blue"`, 1), want: ready.ErrCorrupt},
		{name: "a time that is not one", content: strings.Replace(good, `"generated_at": "2026-11-02T15:00:00Z"`, `"generated_at": "yesterday"`, 1), want: ready.ErrCorrupt},
		{name: "no root id", content: strings.Replace(good, `"id": "internal",`, `"id": "",`, 1), want: ready.ErrCorrupt},
		{name: "a status that is not one", content: strings.Replace(good, `"status": "verified"`, `"status": "fine"`, 1), want: ready.ErrCorrupt},
		{name: "a wrong type", content: strings.Replace(good, `"survival_capable": true`, `"survival_capable": "yes"`, 1), want: ready.ErrCorrupt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if !tt.missing {
				if err := os.WriteFile(filepath.Join(dir, ready.FileName), []byte(tt.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, _, err := ready.Read(config.OSFS{}, dir)
			switch {
			case tt.missing:
				if !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("err = %v, want not-exist", err)
				}
			case !errors.Is(err, tt.want):
				t.Errorf("err = %v, want %v", err, tt.want)
			}
			// A damaged file is never repaired or removed by reading it.
			if !tt.missing {
				if b, _ := os.ReadFile(filepath.Join(dir, ready.FileName)); string(b) != tt.content {
					t.Error("Read must not touch the file")
				}
			}
		})
	}
}

// TestParseAcceptsUnknownKeys: a newer agent may have written more; a reader of schema 1 keeps working.
func TestParseAcceptsUnknownKeys(t *testing.T) {
	good := string(readGolden(t, "fresh-internal.json"))
	extended := strings.Replace(good, `"overall": "green",`, `"overall": "green", "future_field": {"a": 1},`, 1)
	rep, err := ready.Parse([]byte(extended))
	if err != nil || rep.Overall != ready.Green {
		t.Errorf("Parse = %v %v", rep.Overall, err)
	}
}

func TestReadTooBig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ready.FileName), []byte(strings.Repeat(" ", 5<<20)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ready.Read(config.OSFS{}, dir); !errors.Is(err, ready.ErrCorrupt) {
		t.Errorf("err = %v, want ErrCorrupt", err)
	}
}

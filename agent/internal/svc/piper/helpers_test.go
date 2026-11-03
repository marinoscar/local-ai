package piper

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// fakeCopies is an ark.CopyFinder over a map; an id that is not in it is not present.
type fakeCopies map[string]ark.Copy

func (f fakeCopies) BestCopy(id string) (ark.Copy, error) {
	if c, ok := f[id]; ok {
		return c, nil
	}
	return ark.Copy{}, ark.ErrNotPresent
}

// voiceCopy writes the two files of a voice below dir and returns the copy the ark index would hand
// out for it. The model file holds placeholder bytes: the fake streamer never reads it.
func voiceCopy(t testing.TB, dir, id string, rate int) ark.Copy {
	t.Helper()
	d := filepath.Join(dir, id)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	onnx := filepath.Join(d, id+".onnx")
	cfg := filepath.Join(d, id+".onnx.json")
	if err := os.WriteFile(onnx, []byte("model"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf(`{"audio":{"sample_rate":%d},"num_speakers":1}`, rate)), 0o644); err != nil {
		t.Fatal(err)
	}
	return ark.Copy{
		RootID: "internal", Dir: d, Files: []string{onnx, cfg},
		Manifest: manifest.Manifest{
			ItemID: id, ItemKind: manifest.KindModel, Engine: "piper",
			Files: []manifest.File{
				{Kind: FileKindONNX, Path: id + ".onnx"},
				{Kind: FileKindConfig, Path: id + ".onnx.json"},
			},
		},
	}
}

// catalogVoices is the initial catalog of the issue: one English and one Spanish voice.
func catalogVoices() StaticCatalog {
	return StaticCatalog{
		{ID: "piper-en-medium", Language: "en", DisplayName: "English (US), medium"},
		{ID: "piper-es-medium", Language: "es", DisplayName: "Spanish (Latin America), medium"},
	}
}

// installedVoices returns copies for the ids given, all at 22 050 Hz.
func installedVoices(t testing.TB, ids ...string) fakeCopies {
	t.Helper()
	dir := t.TempDir()
	out := fakeCopies{}
	for _, id := range ids {
		out[id] = voiceCopy(t, dir, id, 22050)
	}
	return out
}

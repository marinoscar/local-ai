package router

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
)

func TestDesiredModels(t *testing.T) {
	p := &catalog.Profile{Include: catalog.Include{Models: []string{"a", "b"}}}
	cases := []struct {
		name string
		p    *catalog.Profile
		ov   config.ProfileOverrides
		want []string
	}{
		{"profile only", p, config.ProfileOverrides{}, []string{"a", "b"}},
		{"add", p, config.ProfileOverrides{AddModels: []string{"c"}}, []string{"a", "b", "c"}},
		{"remove", p, config.ProfileOverrides{RemoveModels: []string{"a"}}, []string{"b"}},
		{"remove wins over add", p, config.ProfileOverrides{AddModels: []string{"c"}, RemoveModels: []string{"c", "b"}}, []string{"a"}},
		{"no profile", nil, config.ProfileOverrides{AddModels: []string{"z"}}, []string{"z"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for id := range DesiredModels(tc.p, tc.ov) {
				got = append(got, id)
			}
			sortStrings(got)
			equalStrings(t, got, tc.want)
		})
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func TestRootsOrdered(t *testing.T) {
	r := Roots{Internal: "/srv/localai", Drives: []string{"/media/z", "/media/a", "", "/srv/localai", "/media/m"}}
	equalStrings(t, r.Ordered(), []string{"/srv/localai", "/media/a", "/media/m", "/media/z"})
	equalStrings(t, Roots{Drives: []string{"/media/b", "/media/a"}}.Ordered(), []string{"/media/a", "/media/b"})
	if got := (Roots{}).Ordered(); len(got) != 0 {
		t.Fatalf("no roots: %v", got)
	}
}

// manifestFor writes MANIFEST.json for m under root, with edits applied to the decoded document.
func manifestFor(fs *memFS, root string, m catalog.Model, edit func(map[string]any)) {
	installModel(fs, root, m)
	p := modelDir(root, m.ID) + "/MANIFEST.json"
	raw, _ := fs.get(p)
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	edit(doc)
	out, _ := json.Marshal(doc)
	fs.put(p, out, 0o644)
}

func TestManifestPresence(t *testing.T) {
	m := aiN3Models()[1] // weights + mmproj
	files := func(doc map[string]any) []any { return doc["files"].([]any) }
	cases := []struct {
		name string
		edit func(map[string]any)
		want bool
	}{
		{"verified and complete", func(d map[string]any) {}, true},
		{"sha in capitals", func(d map[string]any) {
			f := files(d)[0].(map[string]any)
			f["sha256"] = strings.ToUpper(f["sha256"].(string))
		}, true},
		{"unknown fields are ignored", func(d map[string]any) { d["future_field"] = []int{1} }, true},
		{"state is not verified", func(d map[string]any) { d["state"] = "downloading" }, false},
		{"state missing", func(d map[string]any) { delete(d, "state") }, false},
		{"verified_at missing", func(d map[string]any) { delete(d, "verified_at") }, false},
		{"verified_at garbage", func(d map[string]any) { d["verified_at"] = "yesterday" }, false},
		{"another model's manifest", func(d map[string]any) { d["item_id"] = "other-model" }, false},
		{"sha differs", func(d map[string]any) { files(d)[0].(map[string]any)["sha256"] = sum("tampered") }, false},
		{"sha empty", func(d map[string]any) { files(d)[0].(map[string]any)["sha256"] = "" }, false},
		{"a catalog file is missing from the manifest", func(d map[string]any) { d["files"] = files(d)[:1] }, false},
		{"path differs", func(d map[string]any) { files(d)[1].(map[string]any)["path"] = "renamed.gguf" }, false},
		{"kind differs", func(d map[string]any) { files(d)[1].(map[string]any)["kind"] = "weights" }, false},
		{"no files at all", func(d map[string]any) { d["files"] = []any{} }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newMemFS()
			manifestFor(fs, "/srv/localai", m, tc.edit)
			got := ManifestPresence{FS: fs}.Present("/srv/localai", &m)
			if got != tc.want {
				t.Fatalf("Present = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("no manifest", func(t *testing.T) {
		if (ManifestPresence{FS: newMemFS()}).Present("/srv/localai", &m) {
			t.Fatal("present without a manifest")
		}
	})
	t.Run("not json", func(t *testing.T) {
		fs := newMemFS()
		fs.put(modelDir("/srv/localai", m.ID)+"/MANIFEST.json", []byte("{not json"), 0o644)
		if (ManifestPresence{FS: fs}).Present("/srv/localai", &m) {
			t.Fatal("present with a broken manifest")
		}
	})
	t.Run("too large", func(t *testing.T) {
		fs := newMemFS()
		fs.put(modelDir("/srv/localai", m.ID)+"/MANIFEST.json", []byte(strings.Repeat(" ", maxManifestBytes+1)), 0o644)
		if (ManifestPresence{FS: fs}).Present("/srv/localai", &m) {
			t.Fatal("an oversized manifest was read")
		}
	})
	t.Run("unreadable", func(t *testing.T) {
		fs := newMemFS()
		installModel(fs, "/srv/localai", m)
		fs.failOp["readfile"] = errors.New("EIO")
		if (ManifestPresence{FS: fs}).Present("/srv/localai", &m) {
			t.Fatal("present although the manifest could not be read")
		}
	})
	t.Run("unpinned catalog entry", func(t *testing.T) {
		fs := newMemFS()
		unpinned := m
		unpinned.Files = append([]catalog.ModelFile(nil), m.Files...)
		unpinned.Files[0].SHA256 = ""
		installModel(fs, "/srv/localai", m)
		if (ManifestPresence{FS: fs}).Present("/srv/localai", &unpinned) {
			t.Fatal("a file without a pinned sha256 cannot be verified")
		}
	})
	t.Run("nil arguments", func(t *testing.T) {
		if (ManifestPresence{}).Present("/srv/localai", &m) || (ManifestPresence{FS: newMemFS()}).Present("", &m) ||
			(ManifestPresence{FS: newMemFS()}).Present("/srv/localai", nil) {
			t.Fatal("nil arguments must not be present")
		}
	})
}

// effective runs EffectiveSet over a fake disk.
func effective(w *lab) Set {
	return EffectiveSet(EffectiveInput{
		Models: w.models, Profile: w.profile, Overrides: w.overrides,
		Roots: w.roots, Presence: ManifestPresence{FS: w.fs}, Fit: w.fit,
	})
}

func roots(set Set) map[string]string {
	out := map[string]string{}
	for _, e := range set.Entries {
		out[e.Model.ID] = e.Root
	}
	return out
}

func TestEffectiveSet(t *testing.T) {
	const nine, vlm = "qwen3.5-9b-q4km", "small-vlm-q4km"

	t.Run("profile models present on the internal root", func(t *testing.T) {
		w := newLab(t)
		set := effective(w)
		equalStrings(t, set.IDs(), []string{nine, vlm})
		if !set.Entries[0].Desired || set.Entries[0].Root != rootInternal {
			t.Fatalf("entry: %+v", set.Entries[0])
		}
		if len(set.Excluded) != 0 {
			t.Fatalf("excluded: %v", set.Excluded)
		}
	})

	t.Run("profile model that is not on disk is excluded with the files reason", func(t *testing.T) {
		w := newLab(t)
		w.fs = newMemFS()
		installModel(w.fs, rootInternal, w.models[0])
		set := effective(w)
		equalStrings(t, set.IDs(), []string{nine})
		if len(set.Excluded) != 1 || set.Excluded[0].ID != vlm || set.Excluded[0].Reason.Code != CodeFilesMissing {
			t.Fatalf("excluded: %+v", set.Excluded)
		}
	})

	t.Run("add_models puts a model in scope", func(t *testing.T) {
		w := newLab(t)
		w.profile.Include.Models = []string{nine}
		w.overrides.AddModels = []string{vlm}
		set := effective(w)
		equalStrings(t, set.IDs(), []string{nine, vlm})
		if !set.Entries[1].Desired {
			t.Fatal("an added model is desired")
		}
	})

	t.Run("a present model outside the profile stays in scope but is not desired", func(t *testing.T) {
		w := newLab(t)
		w.profile.Include.Models = []string{nine}
		set := effective(w)
		equalStrings(t, set.IDs(), []string{nine, vlm})
		if set.Entries[1].Desired {
			t.Fatal("small-vlm is not in the desired set")
		}
	})

	t.Run("remove_models of a model still on disk keeps its preset", func(t *testing.T) {
		w := newLab(t)
		w.overrides.RemoveModels = []string{vlm}
		set := effective(w)
		equalStrings(t, set.IDs(), []string{nine, vlm})
		if set.Entries[1].Desired {
			t.Fatal("a removed model is not desired")
		}
	})

	t.Run("remove_models of a model that is not on disk drops it", func(t *testing.T) {
		w := newLab(t)
		w.fs = newMemFS()
		installModel(w.fs, rootInternal, w.models[0])
		w.overrides.RemoveModels = []string{vlm}
		set := effective(w)
		equalStrings(t, set.IDs(), []string{nine})
		if len(set.Excluded) != 0 {
			t.Fatalf("a removed, absent model is out of scope, not excluded: %v", set.Excluded)
		}
	})

	t.Run("present on a drive only takes the drive path", func(t *testing.T) {
		w := newLab(t)
		w.fs = newMemFS()
		installModel(w.fs, "/media/ark", w.models[0])
		installModel(w.fs, "/media/ark", w.models[1])
		w.roots = Roots{Internal: rootInternal, Drives: []string{"/media/ark"}}
		got := roots(effective(w))
		if got[nine] != "/media/ark" || got[vlm] != "/media/ark" {
			t.Fatalf("roots: %v", got)
		}
	})

	t.Run("present on both takes the internal root", func(t *testing.T) {
		w := newLab(t)
		installModel(w.fs, "/media/ark", w.models[0])
		w.roots = Roots{Internal: rootInternal, Drives: []string{"/media/ark"}}
		if got := roots(effective(w)); got[nine] != rootInternal {
			t.Fatalf("roots: %v", got)
		}
	})

	t.Run("two drives: the first by mount path", func(t *testing.T) {
		w := newLab(t)
		w.fs = newMemFS()
		installModel(w.fs, "/media/b", w.models[0])
		installModel(w.fs, "/media/a", w.models[0])
		w.roots = Roots{Internal: rootInternal, Drives: []string{"/media/b", "/media/a"}}
		if got := roots(effective(w)); got[nine] != "/media/a" {
			t.Fatalf("roots: %v", got)
		}
	})

	t.Run("unverified on the internal root falls through to a drive", func(t *testing.T) {
		w := newLab(t)
		manifestFor(w.fs, rootInternal, w.models[0], func(d map[string]any) { delete(d, "verified_at") })
		installModel(w.fs, "/media/ark", w.models[0])
		w.roots = Roots{Internal: rootInternal, Drives: []string{"/media/ark"}}
		if got := roots(effective(w)); got[nine] != "/media/ark" {
			t.Fatalf("roots: %v", got)
		}
	})

	t.Run("unverified everywhere is excluded", func(t *testing.T) {
		w := newLab(t)
		manifestFor(w.fs, rootInternal, w.models[0], func(d map[string]any) { d["state"] = "downloading" })
		set := effective(w)
		equalStrings(t, set.IDs(), []string{vlm})
		if len(set.Excluded) != 1 || set.Excluded[0].ID != nine || set.Excluded[0].Reason.Code != CodeFilesMissing {
			t.Fatalf("excluded: %+v", set.Excluded)
		}
	})

	t.Run("a static misfit is excluded with the fit reason", func(t *testing.T) {
		w := newLab(t)
		w.models[0].Requirements.MinVRAMMiB = 9000 // the card has 8192, margin 512
		set := effective(w)
		equalStrings(t, set.IDs(), []string{vlm})
		if len(set.Excluded) != 1 || set.Excluded[0].ID != nine || set.Excluded[0].Reason.Code != CodeNotFit {
			t.Fatalf("excluded: %+v", set.Excluded)
		}
	})

	t.Run("other engines are ignored", func(t *testing.T) {
		w := newLab(t)
		w.models = append(w.models, catalog.Model{ID: "whisper-small", Engine: "whisper", Requirements: req(0, 0)})
		w.profile.Include.Models = append(w.profile.Include.Models, "whisper-small")
		equalStrings(t, effective(w).IDs(), []string{nine, vlm})
	})

	t.Run("models out of scope and not present are skipped", func(t *testing.T) {
		w := newLab(t)
		extra := llamaModel("other-model-q4km", []string{"chat"}, 100, 100,
			[]catalog.ModelFile{fileFor("other-model-q4km", "weights", "o.gguf")}, &catalog.Llama{Ctx: 1})
		w.models = append(w.models, extra)
		set := effective(w)
		equalStrings(t, set.IDs(), []string{nine, vlm})
		if len(set.Excluded) != 0 {
			t.Fatalf("excluded: %v", set.Excluded)
		}
	})

	t.Run("entries and exclusions are sorted by id", func(t *testing.T) {
		w := newLab(t)
		w.models[0], w.models[1] = w.models[1], w.models[0]
		equalStrings(t, effective(w).IDs(), []string{nine, vlm})
	})

	t.Run("no roots means nothing is present", func(t *testing.T) {
		w := newLab(t)
		w.roots = Roots{}
		set := effective(w)
		if len(set.Entries) != 0 || len(set.Excluded) != 2 {
			t.Fatalf("set: %+v", set)
		}
	})
}

func TestEffectiveDefault(t *testing.T) {
	prof := &catalog.Profile{DefaultModel: "profile-model", Survival: catalog.Survival{DefaultModel: "survival-model"}}
	ids := []string{"state-model", "profile-model", "survival-model"}
	cases := []struct {
		name     string
		ids      []string
		state    string
		p        *catalog.Profile
		survival bool
		want     string
	}{
		{"state default wins", ids, "state-model", prof, false, "state-model"},
		{"state default without a preset falls to the profile", []string{"profile-model"}, "state-model", prof, false, "profile-model"},
		{"profile default", ids, "", prof, false, "profile-model"},
		{"profile default without a preset, not survival: none", []string{"survival-model"}, "", prof, false, ""},
		{"survival default in survival mode", []string{"survival-model"}, "", prof, true, "survival-model"},
		{"survival mode still prefers the profile default", ids, "", prof, true, "profile-model"},
		{"nothing qualifies", []string{"other"}, "gone", prof, true, ""},
		{"no profile", ids, "state-model", nil, false, "state-model"},
		{"no profile and no state default", ids, "", nil, true, ""},
		{"empty set", nil, "state-model", prof, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveDefault(tc.ids, tc.state, tc.p, tc.survival); got != tc.want {
				t.Fatalf("EffectiveDefault = %q, want %q", got, tc.want)
			}
		})
	}
}

var _ = os.ErrNotExist

// Speech models live under speech/<engine>/<id>, not models/<id> (overview.md section 3.3): a verified
// whisper or piper copy must count as present, and the same manifest under models/ must not.
func TestManifestPresenceFollowsTheEngineDirectory(t *testing.T) {
	const root = "/srv/localai"
	for _, tc := range []struct{ engine, dir string }{
		{"llama", "/models/m1"},
		{"whisper", "/speech/whisper/m1"},
		{"piper", "/speech/piper/m1"},
	} {
		t.Run(tc.engine, func(t *testing.T) {
			m := catalog.Model{ID: "m1", Engine: tc.engine, Files: []catalog.ModelFile{{Name: "f.bin", Kind: "weights", SHA256: sum("f")}}}
			doc, _ := json.Marshal(map[string]any{
				"item_id": "m1", "state": "verified", "verified_at": "2026-11-04T05:19:26Z",
				"files": []any{map[string]any{"kind": "weights", "path": "f.bin", "sha256": sum("f")}},
			})
			fs := newMemFS()
			fs.put(root+tc.dir+"/MANIFEST.json", doc, 0o644)
			if !(ManifestPresence{FS: fs}).Present(root, &m) {
				t.Errorf("a verified %s manifest in %s is not present", tc.engine, tc.dir)
			}
			other := newMemFS()
			wrong := "/models/m1"
			if tc.engine == "llama" {
				wrong = "/speech/whisper/m1"
			}
			other.put(root+wrong+"/MANIFEST.json", doc, 0o644)
			if (ManifestPresence{FS: other}).Present(root, &m) {
				t.Errorf("a %s manifest in %s must not count", tc.engine, wrong)
			}
		})
	}
}

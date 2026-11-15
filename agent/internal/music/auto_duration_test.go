package music_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/music"
)

// Auto length (#571): duration_s left out, empty or "auto" sends null to the engine, the disk guard counts
// the 240 s cap, and meta.json records the real length with requested_duration_s null.
func TestAutoDurationReachesTheEngineAsNull(t *testing.T) {
	for name, over := range map[string]map[string]string{
		"omitted": {"duration_s": "-"},
		"empty":   {"duration_s": ""},
		"auto":    {"duration_s": "auto"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.shim.AutoComplete = true
			rec := r.generate(form{fields: baseFields(over)})
			if rec.Code != http.StatusAccepted {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			acc := decode[music.Accepted](t, rec)
			v := r.waitOp(acc.OpID)
			if v.State != music.StateDone {
				t.Fatalf("op = %+v", v)
			}
			if d := r.shim.Requests[0].DurationS; d != nil {
				t.Fatalf("the engine was asked for %d s, want Auto (null)", *d)
			}
			m, _, err := r.svc.Song(v.SongIDs[0])
			if err != nil {
				t.Fatal(err)
			}
			if m.Requested != nil || m.DurationS <= 0 {
				t.Fatalf("meta requested %v, duration %v", m.Requested, m.DurationS)
			}
			raw, err := os.ReadFile(filepath.Join(r.songs, v.SongIDs[0], "meta.json"))
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			if got, ok := doc["requested_duration_s"]; !ok || got != nil {
				t.Fatalf("meta.json requested_duration_s = %v (present %v), want null", got, ok)
			}
			if got, _ := doc["duration_s"].(float64); got != m.DurationS {
				t.Fatalf("meta.json duration_s = %v", doc["duration_s"])
			}
			r.noSecretsInLogs()
		})
	}
}

func TestExplicitDurationIsRecorded(t *testing.T) {
	r := newRig(t)
	r.shim.AutoComplete = true
	acc := decode[music.Accepted](t, r.generate(form{fields: baseFields(nil)}))
	v := r.waitOp(acc.OpID)
	m, _, err := r.svc.Song(v.SongIDs[0])
	if err != nil || m.Requested == nil || *m.Requested != 30 {
		t.Fatalf("requested %v (%v)", m.Requested, err)
	}
	if d := r.shim.Requests[0].DurationS; d == nil || *d != 30 {
		t.Fatalf("engine duration %v", d)
	}
}

func TestDiskGuardCountsTheCapForAuto(t *testing.T) {
	// 2 versions: 30 s need 11.5 MB, Auto counts 240 s (92 MB). With 60 MB free (1 MiB kept free) only the
	// explicit length fits, and Auto with a single version (46 MB).
	const free = 60_000_000
	for _, c := range []struct {
		name   string
		over   map[string]string
		status int
	}{
		{"explicit 30 s fits", nil, http.StatusAccepted},
		{"auto counts 240 s", map[string]string{"duration_s": "-"}, http.StatusInsufficientStorage},
		{"explicit 240 s does not fit", map[string]string{"duration_s": "240"}, http.StatusInsufficientStorage},
		{"auto with one version fits", map[string]string{"duration_s": "-", "variations": "1"}, http.StatusAccepted},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.setFree(free)
			rec := r.generate(form{fields: baseFields(c.over)})
			if c.status == http.StatusInsufficientStorage {
				expectError(t, rec, c.status, doctor.SVC155)
				if len(r.songDirs()) != 0 || len(r.workEntries()) != 0 {
					t.Fatal("a refused request left files")
				}
				return
			}
			if rec.Code != c.status {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

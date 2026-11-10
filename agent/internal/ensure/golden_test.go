package ensure

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/kit"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// The body of GET /ensure is a contract (agent.md section 3.8.33): the UI, the CLI and the cloud read these
// keys. A golden file per situation and language keeps the shape from drifting.
func TestViewGolden(t *testing.T) {
	cases := []struct {
		name string
		lang string
		set  func(r *rig)
	}{
		{"complete", "en", func(r *rig) {
			r.machine.tools = map[string]string{kit.ToolKiwix: "3.7.0", kit.ToolPmtiles: "1.22", kit.ToolPiper: "2023.11.14-2"}
			r.machine.missing, r.machine.whisper = nil, true
		}},
		{"ask-en", "en", func(r *rig) { r.auto = AutoAsk }},
		{"ask-es", "es", func(r *rig) { r.auto = AutoAsk }},
		{"off-en", "en", func(r *rig) { r.auto = AutoOff }},
		{"confirm-en", "en", func(r *rig) {
			r.wants = Wants{Whisper: true, WhisperWant: WhisperWant{Backend: "cpu"}}
			r.kit.prebuiltErr, r.kit.src = ErrKitMissing, true
		}},
		{"blocked-es", "es", func(r *rig) {
			r.wants = Wants{Tools: map[string]bool{kit.ToolKiwix: true}}
			r.kit.tools = map[string]toolOffer{}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.clock.frozen = true
			tc.set(r)
			r.build()
			got, err := json.MarshalIndent(r.svc.View(bg, tc.lang), "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join("testdata", "view-"+tc.name+".json")
			if *update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run go test -update)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs from the golden file (run go test -update to accept):\n%s", path, got)
			}
		})
	}
}

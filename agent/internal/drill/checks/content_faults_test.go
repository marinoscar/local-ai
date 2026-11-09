package checks

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/portal"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
	"github.com/marinoscar/local-ai/agent/internal/svc/piper"
)

// multiAgent serves one request from the first fake whose routes match it.
type multiAgent []*fakeAgent

func (m multiAgent) Do(req *http.Request) (*http.Response, error) {
	for _, a := range m {
		if _, pattern := a.mux.Handler(req); pattern != "" {
			return a.Do(req)
		}
	}
	return (&fakeAgent{mux: http.NewServeMux(), down: map[string]bool{}}).Do(req)
}

// faults are the one-at-a-time failures of the integration tests of DRILL-04.
type faults struct {
	stopKiwix    bool
	whisperDown  bool
	noGlyphs     bool
	corruptModel bool
	dropDocs     string // a docs file to delete
	dropCardPDF  bool
}

// world is a healthy node: content on the internal root, the manuals, and every service answering.
type world struct {
	r      *contentRig
	agent  multiAgent
	checks []drill.Check
}

func buildWorld(t *testing.T, f faults) *world {
	t.Helper()
	r := newContentRig(t)
	r.installNode()

	// The manuals: the card PDF is made after the item was hashed, so it is not in the manifest.
	files := docsFiles()
	pdf := files["survival-card-ai-test.pdf"]
	delete(files, "survival-card-ai-test.pdf")
	docsDir := r.installDocs(files, testCatalog)
	mustWrite(t, filepath.Join(docsDir, "survival-card-ai-test.pdf"), pdf)

	// The library: a tier-1 and a tier-2 book, and the first-aid article.
	ok := text(200, "application/rss+xml", rssOne)
	lib := kiwixAgent(map[string]http.HandlerFunc{"wikimed_en": ok, "wikibooks_en": ok})
	lib.handle("/library/content/", status(200))
	lib.take("/library", f.stopKiwix)

	// Speech: the recordings are understood, the voices speak, the round trip comes back.
	stt := map[string]sttSpec{
		"en":    {text: enSentence, language: "en"},
		"es":    {text: esSentence, language: "es"},
		"other": {text: "Test of the emergency voice.", language: "en"},
	}
	if f.whisperDown {
		stt = map[string]sttSpec{"*": {status: 503, raw: `{"error":{"code":"LAI-SVC-011"}}`}}
	}
	voice := map[string]string{piper.HeaderVoice: "voice"}
	sp := newSpeechAgent(t, stt, map[string]http.HandlerFunc{"en": wavReply(t, 2.1, 0.5, voice), "es": wavReply(t, 2.6, 0.4, voice)})

	// Maps.
	mf := okMaps(t, nil)
	mf.noGlyphs = f.noGlyphs
	mp := mf.agent()
	mp.handle(mapsSearchPath, jsonOf(200, map[string]any{"results": []map[string]any{{"name": "San José"}}}))

	// The Portal and the chat page.
	pp := goodPortal()
	pp.status = jsonOf(200, portal.Status{
		SchemaVersion: portal.SchemaVersion, GeneratedAt: "2026-11-02T18:04:11Z", NodeID: testNode, Mode: "online",
		FirstAid: []portal.FirstAid{{Key: "bleeding", Title: "Bleeding", URL: "/library/content/wikimed_en/A/Bleeding", Available: true}},
	})
	pa := pp.agent()

	w := &world{r: r, agent: multiAgent{lib, sp.fakeAgent, mp, pa}}
	w.checks = ContentChecks(ContentDeps{
		HTTP: w.agent, Roots: r.rootList, FS: config.OSFS{}, Ready: r.ready, Verifier: r.rv, ModelEngine: engineOf,
		NodeKit: func(context.Context) []string { return []string{refKit.ID} },
		Books: books(
			LibraryBook{Name: "wikimed_en", ItemID: refZimTier1.ID, Tier: 1},
			LibraryBook{Name: "wikibooks_en", ItemID: refZimTier2.ID, Tier: 2},
		),
		NodeID: func() string { return testNode }, CatalogVersion: func() string { return testCatalog },
		Health: fakeHealth{records: []health.Record{rec("kiwix", health.StateOK), rec("whisper", health.StateOK)}},
	})

	if f.dropDocs != "" {
		mustRemove(t, filepath.Join(docsDir, f.dropDocs))
	}
	if f.dropCardPDF {
		mustRemove(t, filepath.Join(docsDir, "survival-card-ai-test.pdf"))
	}
	if f.corruptModel {
		file := filepath.Join(r.root(ark.InternalID).Path, "models", refSurvivalModel.ID, "data.bin")
		b := mustRead(t, file)
		b[len(b)/3] ^= 0x01
		mustWrite(t, file, b)
	}
	return w
}

// results runs every check of the world, in order, and returns result and code by id.
func (w *world) results(t *testing.T) map[string]drill.Outcome {
	t.Helper()
	out := map[string]drill.Outcome{}
	for _, c := range w.checks {
		out[c.ID] = c.Run(bg, testEnv(false))
	}
	return out
}

// TestHealthyWorldPassesEverything is the baseline of the fault injection: with nothing broken every
// check of DRILL-04 passes.
func TestHealthyWorldPassesEverything(t *testing.T) {
	w := buildWorld(t, faults{})
	for id, o := range w.results(t) {
		if o.Result != drill.ResultPass {
			t.Errorf("%s = %+v, want pass", id, o)
		}
	}
}

// TestFaultInjectionOneAtATime breaks one thing in an otherwise healthy node and asserts which checks
// change and with which doctor code (drill.md section 9.2). Every check not named stays a pass, so a
// fault shows up only where it should.
func TestFaultInjectionOneAtATime(t *testing.T) {
	type want struct {
		result drill.Result
		code   string
	}
	cases := []struct {
		name   string
		fault  faults
		expect map[string]want
	}{
		{"a model byte is corrupt", faults{corruptModel: true}, map[string]want{
			"drl.files.hash": {drill.ResultFail, doctor.DRL012},
		}},
		{"kiwix is stopped", faults{stopKiwix: true}, map[string]want{
			"drl.kiwix.search":    {drill.ResultFail, doctor.DRL030},
			"drl.portal.firstaid": {drill.ResultWarn, doctor.SVC109}, // the article is served by kiwix too
		}},
		{"the map assets are gone", faults{noGlyphs: true}, map[string]want{
			"drl.maps.tile": {drill.ResultWarn, doctor.SVC031},
		}},
		{"docs/index.html is deleted", faults{dropDocs: "index.html"}, map[string]want{
			"drl.docs":           {drill.ResultFail, doctor.DRL060},
			"drl.files.manifest": {drill.ResultFail, doctor.DRL011}, // the docs-bundle item is incomplete too
		}},
		{"docs/06-rebuild-a-node-offline.html is deleted", faults{dropDocs: "06-rebuild-a-node-offline.html"}, map[string]want{
			"drl.docs":           {drill.ResultFail, doctor.DRL060},
			"drl.files.manifest": {drill.ResultFail, doctor.DRL011},
		}},
		{"only the survival card PDF is deleted", faults{dropCardPDF: true}, map[string]want{
			"drl.docs": {drill.ResultWarn, doctor.CLI014},
		}},
		{"whisper is down", faults{whisperDown: true}, map[string]want{
			"drl.stt.en":           {drill.ResultFail, doctor.DRL032},
			"drl.stt.es":           {drill.ResultFail, doctor.DRL032},
			"drl.speech.roundtrip": {drill.ResultWarn, doctor.DRL032},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildWorld(t, tc.fault).results(t)
			for id, o := range got {
				w, broken := tc.expect[id]
				if !broken {
					w = want{drill.ResultPass, ""}
				}
				if o.Result != w.result || o.Code != w.code {
					t.Errorf("%s = %s %q (%v), want %s %q", id, o.Result, o.Code, o.Details, w.result, w.code)
				}
			}
		})
	}
}

const mapsSearchPath = "/maps/search"

func mustRead(t testing.TB, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

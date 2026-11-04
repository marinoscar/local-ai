package portal_test

import (
	"reflect"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/portal"
)

// healthy is the input of a node where every row of the table of survival-portal.md section 4.2 is ready.
func healthy() portal.Inputs {
	return portal.Inputs{
		Lang: "es", Loaded: 1, LoadedVision: true, VisionModel: "vlm", AnyLoadable: true,
		Llama:   portal.Svc{State: portal.SvcOK},
		Whisper: portal.Svc{State: portal.SvcOK}, VoiceLangs: []string{"en", "es"}, Voices: 2,
		Kiwix: portal.Svc{State: portal.SvcOK}, Books: []portal.Book{{ContentID: "wiki-alpha-en", Title: "Wiki", Book: "wiki_en", Lang: "en"}},
		Maps: portal.Svc{State: portal.SvcOK}, HasMap: true,
		Manuals: []portal.Manual{{ID: "01-survival-mode", Title: "Survival mode", URL: "/docs/01-survival-mode.html"}},
	}
}

type want struct{ state, code, reason string }

func TestChatCapability(t *testing.T) {
	cases := []struct {
		name string
		mod  func(*portal.Inputs)
		want want
	}{
		{"loaded and llama ok", func(*portal.Inputs) {}, want{portal.StateReady, "", ""}},
		{"loaded, llama state unknown", func(i *portal.Inputs) { i.Llama = portal.Svc{} }, want{portal.StateReady, "", ""}},
		{"loaded, llama degraded", func(i *portal.Inputs) { i.Llama.State = portal.SvcDegraded }, want{portal.StateDegraded, "", portal.ReasonLlamaDegraded}},
		{"brain loading", func(i *portal.Inputs) { i.Loaded, i.Loading = 0, true }, want{portal.StateDegraded, "", portal.ReasonLoading}},
		{"llama starting, nothing loaded", func(i *portal.Inputs) { i.Loaded = 0; i.Llama.State = portal.SvcStarting }, want{portal.StateDegraded, "", portal.ReasonStarting}},
		{"nothing loaded, a brain is loadable", func(i *portal.Inputs) { i.Loaded = 0 }, want{portal.StateUnavailable, doctor.SVC106, portal.ReasonNotLoaded}},
		{"nothing loadable", func(i *portal.Inputs) { i.Loaded, i.AnyLoadable = 0, false }, want{portal.StateUnavailable, doctor.SVC106, portal.ReasonNoBrain}},
		{"llama down", func(i *portal.Inputs) { i.Llama.State = portal.SvcDown }, want{portal.StateUnavailable, doctor.SVC102, ""}},
		{"llama down beats a loaded brain", func(i *portal.Inputs) { i.Llama.State = portal.SvcDown; i.Loaded = 2 }, want{portal.StateUnavailable, doctor.SVC102, ""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := healthy()
			c.mod(&in)
			got := portal.ComputeCapabilities(in).Chat
			if got.State != c.want.state || got.Code != c.want.code || got.Reason != c.want.reason {
				t.Errorf("chat = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestPhotoCapability(t *testing.T) {
	cases := []struct {
		name   string
		mod    func(*portal.Inputs)
		want   want
		vision string
	}{
		{"a loaded brain has vision", func(*portal.Inputs) {}, want{portal.StateReady, "", ""}, ""},
		{"vision loadable, not loaded", func(i *portal.Inputs) { i.LoadedVision = false }, want{portal.StateDegraded, "", portal.ReasonNeedsSwitch}, "vlm"},
		{"no vision model loadable", func(i *portal.Inputs) { i.LoadedVision, i.VisionModel = false, "" }, want{portal.StateUnavailable, doctor.SVC040, portal.ReasonNoVisionModel}, ""},
		{"vision loaded, llama degraded", func(i *portal.Inputs) { i.Llama.State = portal.SvcDegraded }, want{portal.StateDegraded, "", portal.ReasonLlamaDegraded}, ""},
		{"llama down", func(i *portal.Inputs) { i.Llama.State = portal.SvcDown }, want{portal.StateUnavailable, doctor.SVC102, ""}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := healthy()
			c.mod(&in)
			got := portal.ComputeCapabilities(in).Photo
			if got.State != c.want.state || got.Code != c.want.code || got.Reason != c.want.reason || got.VisionModel != c.vision {
				t.Errorf("photo = %+v, want %+v vision %q", got, c.want, c.vision)
			}
		})
	}
}

func TestTalkCapability(t *testing.T) {
	cases := []struct {
		name string
		mod  func(*portal.Inputs)
		want want
		stt  bool
	}{
		{"whisper ok, voice for the language", func(*portal.Inputs) {}, want{portal.StateReady, "", ""}, true},
		{"no voice for the UI language", func(i *portal.Inputs) { i.VoiceLangs, i.Voices = []string{"en"}, 1 }, want{portal.StateDegraded, doctor.SVC021, portal.ReasonNoVoiceLanguage}, true},
		{"no voice at all: text only", func(i *portal.Inputs) { i.VoiceLangs, i.Voices = nil, 0 }, want{portal.StateDegraded, doctor.SVC022, portal.ReasonNoVoice}, true},
		{"whisper degraded", func(i *portal.Inputs) { i.Whisper.State = portal.SvcDegraded }, want{portal.StateDegraded, "", portal.ReasonServiceDegraded}, true},
		{"whisper starting", func(i *portal.Inputs) { i.Whisper.State = portal.SvcStarting }, want{portal.StateDegraded, "", portal.ReasonStarting}, true},
		{"whisper missing content", func(i *portal.Inputs) { i.Whisper.State = portal.SvcMissingContent }, want{portal.StateUnavailable, doctor.SVC010, ""}, false},
		{"whisper down", func(i *portal.Inputs) { i.Whisper.State = portal.SvcDown }, want{portal.StateUnavailable, doctor.SVC011, ""}, false},
		{"whisper disabled", func(i *portal.Inputs) { i.Whisper.State = portal.SvcDisabled }, want{portal.StateUnavailable, doctor.SVC010, portal.ReasonDisabled}, false},
		{"no brain", func(i *portal.Inputs) { i.Loaded = 0 }, want{portal.StateUnavailable, doctor.SVC106, portal.ReasonNoBrain}, false},
		{"brain loading counts as a brain", func(i *portal.Inputs) { i.Loaded, i.Loading = 0, true }, want{portal.StateReady, "", ""}, true},
		{"Spanish UI, English voice only, English UI is fine", func(i *portal.Inputs) { i.Lang = "en"; i.VoiceLangs, i.Voices = []string{"en"}, 1 }, want{portal.StateReady, "", ""}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := healthy()
			c.mod(&in)
			got := portal.ComputeCapabilities(in).Talk
			if got.State != c.want.state || got.Code != c.want.code || got.Reason != c.want.reason || got.STT != c.stt {
				t.Errorf("talk = %+v, want %+v stt %v", got, c.want, c.stt)
			}
			if got.TTSLanguage == nil {
				t.Error("tts_languages is null, want an array")
			}
		})
	}
}

func TestLibraryCapability(t *testing.T) {
	cases := []struct {
		name string
		mod  func(*portal.Inputs)
		want want
	}{
		{"kiwix ok with a book", func(*portal.Inputs) {}, want{portal.StateReady, "", ""}},
		{"a book was skipped", func(i *portal.Inputs) {
			i.Kiwix = portal.Svc{State: portal.SvcDegraded, Problems: []string{doctor.SVC004}}
		}, want{portal.StateDegraded, doctor.SVC004, portal.ReasonServiceDegraded}},
		{"degraded without a known code", func(i *portal.Inputs) { i.Kiwix.State = portal.SvcDegraded }, want{portal.StateDegraded, "", portal.ReasonServiceDegraded}},
		{"starting", func(i *portal.Inputs) { i.Kiwix.State = portal.SvcStarting }, want{portal.StateDegraded, "", portal.ReasonStarting}},
		{"no books", func(i *portal.Inputs) { i.Books = nil }, want{portal.StateUnavailable, doctor.SVC001, portal.ReasonNoBooks}},
		{"missing content", func(i *portal.Inputs) { i.Kiwix.State = portal.SvcMissingContent }, want{portal.StateUnavailable, doctor.SVC001, portal.ReasonNoBooks}},
		{"kiwix down", func(i *portal.Inputs) { i.Kiwix.State = portal.SvcDown }, want{portal.StateUnavailable, doctor.SVC002, ""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := healthy()
			c.mod(&in)
			got := portal.ComputeCapabilities(in).Library
			if got.State != c.want.state || got.Code != c.want.code || got.Reason != c.want.reason {
				t.Errorf("library = %+v, want %+v", got, c.want)
			}
			if got.Books == nil {
				t.Error("books is null, want an array")
			}
		})
	}
}

func TestMapsCapability(t *testing.T) {
	cases := []struct {
		name   string
		mod    func(*portal.Inputs)
		want   want
		search bool
	}{
		{"a map and assets", func(*portal.Inputs) {}, want{portal.StateReady, "", ""}, true},
		{"no assets", func(i *portal.Inputs) {
			i.Maps = portal.Svc{State: portal.SvcDegraded, Problems: []string{doctor.SVC031}}
		}, want{portal.StateDegraded, doctor.SVC031, portal.ReasonNoAssets}, true},
		{"no search", func(i *portal.Inputs) {
			i.Maps = portal.Svc{State: portal.SvcDegraded, Problems: []string{doctor.SVC032}}
		}, want{portal.StateDegraded, doctor.SVC032, portal.ReasonNoSearch}, false},
		{"no assets and no search: assets first", func(i *portal.Inputs) {
			i.Maps = portal.Svc{State: portal.SvcDegraded, Problems: []string{doctor.SVC032, doctor.SVC031}}
		}, want{portal.StateDegraded, doctor.SVC031, portal.ReasonNoAssets}, false},
		{"degraded for another reason", func(i *portal.Inputs) { i.Maps.State = portal.SvcDegraded }, want{portal.StateDegraded, "", portal.ReasonServiceDegraded}, true},
		{"no map", func(i *portal.Inputs) { i.HasMap = false }, want{portal.StateUnavailable, doctor.SVC030, portal.ReasonNoMap}, false},
		{"missing content", func(i *portal.Inputs) { i.Maps.State = portal.SvcMissingContent }, want{portal.StateUnavailable, doctor.SVC030, portal.ReasonNoMap}, false},
		{"liveness failed", func(i *portal.Inputs) { i.Maps.State = portal.SvcDown }, want{portal.StateUnavailable, doctor.SVC034, ""}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := healthy()
			c.mod(&in)
			got := portal.ComputeCapabilities(in).Maps
			if got.State != c.want.state || got.Code != c.want.code || got.Reason != c.want.reason || got.Search != c.search {
				t.Errorf("maps = %+v, want %+v search %v", got, c.want, c.search)
			}
			if got.GPS {
				t.Error("gps is true: no position source is wired")
			}
		})
	}
}

func TestManualsCapability(t *testing.T) {
	cases := []struct {
		name string
		mod  func(*portal.Inputs)
		want want
	}{
		{"docs present", func(*portal.Inputs) {}, want{portal.StateReady, "", ""}},
		{"rendered without the inventory", func(i *portal.Inputs) { i.ManualsNoInventory = true }, want{portal.StateDegraded, doctor.SYN062, portal.ReasonNoInventory}},
		{"no docs: no code, the SPA opens the built-in help", func(i *portal.Inputs) { i.Manuals = nil }, want{portal.StateUnavailable, "", portal.ReasonNoDocs}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := healthy()
			c.mod(&in)
			got := portal.ComputeCapabilities(in).Manuals
			if got.State != c.want.state || got.Code != c.want.code || got.Reason != c.want.reason {
				t.Errorf("manuals = %+v, want %+v", got, c.want)
			}
			if got.Items == nil {
				t.Error("items is null, want an array")
			}
		})
	}
}

// TestComputeCapabilitiesIsPure: the same inputs give the same answer and the input is not changed.
func TestComputeCapabilitiesIsPure(t *testing.T) {
	in := healthy()
	before := healthy()
	a, b := portal.ComputeCapabilities(in), portal.ComputeCapabilities(in)
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(in, before) {
		t.Error("ComputeCapabilities is not pure")
	}
}

// TestCapabilityCodesAreRegistered: every code the model emits is in the doctor registry.
func TestCapabilityCodesAreRegistered(t *testing.T) {
	for _, code := range []string{doctor.SVC102, doctor.SVC106, doctor.SVC040, doctor.SVC010, doctor.SVC011, doctor.SVC022, doctor.SVC021,
		doctor.SVC001, doctor.SVC002, doctor.SVC004, doctor.SVC030, doctor.SVC031, doctor.SVC032, doctor.SVC034, doctor.SYN062,
		doctor.SVC111, doctor.SVC112, doctor.SVC113} {
		if _, ok := doctor.Lookup(code); !ok {
			t.Errorf("%s is not in the registry", code)
		}
	}
}

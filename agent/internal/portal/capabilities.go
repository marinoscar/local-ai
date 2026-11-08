package portal

import "github.com/marinoscar/local-ai/agent/internal/doctor"

// Service states the capability model reads (companion-services.md section 4.6). The empty string
// means "no supervisor view": the capability then rests on the other inputs only.
const (
	SvcOK             = "ok"
	SvcDegraded       = "degraded"
	SvcDown           = "down"
	SvcMissingContent = "missing_content"
	SvcDisabled       = "disabled"
	SvcStarting       = "starting"
)

// Reasons of a capability that is not ready.
const (
	ReasonLoading         = "loading"
	ReasonStarting        = "starting"
	ReasonLlamaDegraded   = "llama_degraded"
	ReasonNoBrain         = "no_brain"
	ReasonNotLoaded       = "not_loaded"
	ReasonNeedsSwitch     = "needs_switch"
	ReasonNoVisionModel   = "no_vision_model"
	ReasonDisabled        = "disabled"
	ReasonServiceDegraded = "service_degraded"
	ReasonNoVoiceLanguage = "no_voice_for_language"
	ReasonNoVoice         = "no_voice"
	ReasonNoBooks         = "no_books"
	ReasonNoMap           = "no_map"
	ReasonNoAssets        = "no_assets"
	ReasonNoSearch        = "no_search"
	ReasonNoInventory     = "no_inventory"
	ReasonNoDocs          = "no_docs"
)

// Svc is what the capability model needs of one supervised service.
type Svc struct {
	// State is one of the Svc* values, or "" when unknown.
	State string
	// Problems are the doctor codes the supervisor reports for it.
	Problems []string
}

func (s Svc) has(code string) bool {
	for _, p := range s.Problems {
		if p == code {
			return true
		}
	}
	return false
}

// Book is an entry of capabilities.library.books. Book is the undated kiwix book name.
type Book struct {
	ContentID string `json:"content_id"`
	Title     string `json:"title"`
	Book      string `json:"book"`
	Lang      string `json:"lang"`
}

// Manual is an entry of capabilities.manuals.items.
type Manual struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

// Inputs are the facts ComputeCapabilities decides from. Every field is a cached value: no check
// runs for a request.
type Inputs struct {
	// Lang is the UI language, en or es.
	Lang string

	// Loaded and Loading describe the brain; LoadedVision is true when a loaded model has vision.
	Loaded       int
	Loading      bool
	LoadedVision bool
	// AnyLoadable is true when at least one llama model could be loaded now.
	AnyLoadable bool
	// VisionModel is the model that would answer a photo when none loaded does ("" for none).
	VisionModel string
	// Llama is the state of the router service.
	Llama Svc

	Whisper Svc
	// VoiceLangs are the languages (en, es, ...) with an installed voice.
	VoiceLangs []string
	// Voices counts every installed voice.
	Voices int

	Kiwix Svc
	Books []Book

	Maps Svc
	// HasMap is true when a map is installed.
	HasMap bool

	// Manuals are the rendered documents; ManualsNoInventory is true when they were rendered
	// without the lab inventory (LAI-SYN-062).
	Manuals            []Manual
	ManualsNoInventory bool
}

// ChatCap is capabilities.chat.
type ChatCap struct {
	State  string `json:"state"`
	Code   string `json:"code,omitempty"`
	Action string `json:"action,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// PhotoCap is capabilities.photo.
type PhotoCap struct {
	State       string `json:"state"`
	Code        string `json:"code,omitempty"`
	Action      string `json:"action,omitempty"`
	Reason      string `json:"reason,omitempty"`
	VisionModel string `json:"vision_model,omitempty"`
}

// TalkCap is capabilities.talk.
type TalkCap struct {
	State       string   `json:"state"`
	Code        string   `json:"code,omitempty"`
	Action      string   `json:"action,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	STT         bool     `json:"stt"`
	TTSLanguage []string `json:"tts_languages"`
}

// LibraryCap is capabilities.library.
type LibraryCap struct {
	State  string `json:"state"`
	Code   string `json:"code,omitempty"`
	Action string `json:"action,omitempty"`
	Reason string `json:"reason,omitempty"`
	Books  []Book `json:"books"`
}

// MapsCap is capabilities.maps.
type MapsCap struct {
	State  string `json:"state"`
	Code   string `json:"code,omitempty"`
	Action string `json:"action,omitempty"`
	Reason string `json:"reason,omitempty"`
	Search bool   `json:"search"`
	// GPS is false: no position source is wired into the Portal yet.
	GPS bool `json:"gps"`
}

// ManualsCap is capabilities.manuals.
type ManualsCap struct {
	State  string   `json:"state"`
	Code   string   `json:"code,omitempty"`
	Action string   `json:"action,omitempty"`
	Reason string   `json:"reason,omitempty"`
	Items  []Manual `json:"items"`
}

// Capabilities are the six buttons of the home screen (survival-portal.md section 4.2).
type Capabilities struct {
	Chat    ChatCap    `json:"chat"`
	Photo   PhotoCap   `json:"photo"`
	Talk    TalkCap    `json:"talk"`
	Library LibraryCap `json:"library"`
	Maps    MapsCap    `json:"maps"`
	Manuals ManualsCap `json:"manuals"`
}

// ComputeCapabilities implements every row of the table of survival-portal.md section 4.2. It is a
// pure function of in. A code is set only when a problem causes the state: needs_switch has none.
func ComputeCapabilities(in Inputs) Capabilities {
	brain := in.Loaded > 0 || in.Loading
	return Capabilities{
		Chat:    computeChat(in),
		Photo:   computePhoto(in),
		Talk:    computeTalk(in, brain),
		Library: computeLibrary(in),
		Maps:    computeMaps(in),
		Manuals: computeManuals(in),
	}
}

// WithActions sets the action of every capability whose code has one (doctor-codes.md section 1.7):
// the Unavailable screen shows it as the primary button. ComputeCapabilities stays free of it, so
// the table of section 4.2 is tested on its own.
func WithActions(c Capabilities) Capabilities {
	act := func(code string) string { return string(doctor.ActionOf(code)) }
	c.Chat.Action = act(c.Chat.Code)
	c.Photo.Action = act(c.Photo.Code)
	c.Talk.Action = act(c.Talk.Code)
	c.Library.Action = act(c.Library.Code)
	c.Maps.Action = act(c.Maps.Code)
	c.Manuals.Action = act(c.Manuals.Code)
	return c
}

func computeChat(in Inputs) ChatCap {
	switch {
	case in.Llama.State == SvcDown || in.Llama.State == SvcMissingContent:
		return ChatCap{State: StateUnavailable, Code: doctor.SVC102}
	case in.Loaded > 0 && in.Llama.State == SvcDegraded:
		return ChatCap{State: StateDegraded, Reason: ReasonLlamaDegraded}
	case in.Loaded > 0:
		return ChatCap{State: StateReady}
	case in.Loading:
		return ChatCap{State: StateDegraded, Reason: ReasonLoading}
	case in.Llama.State == SvcStarting:
		return ChatCap{State: StateDegraded, Reason: ReasonStarting}
	}
	reason := ReasonNoBrain
	if in.AnyLoadable {
		reason = ReasonNotLoaded // a brain exists on disk: the Portal offers "Switch brain"
	}
	return ChatCap{State: StateUnavailable, Code: doctor.SVC106, Reason: reason}
}

func computePhoto(in Inputs) PhotoCap {
	switch {
	case in.Llama.State == SvcDown || in.Llama.State == SvcMissingContent:
		return PhotoCap{State: StateUnavailable, Code: doctor.SVC102}
	case in.LoadedVision && in.Llama.State == SvcDegraded:
		return PhotoCap{State: StateDegraded, Reason: ReasonLlamaDegraded}
	case in.LoadedVision:
		return PhotoCap{State: StateReady}
	case in.VisionModel != "":
		return PhotoCap{State: StateDegraded, Reason: ReasonNeedsSwitch, VisionModel: in.VisionModel}
	}
	return PhotoCap{State: StateUnavailable, Code: doctor.SVC040, Reason: ReasonNoVisionModel}
}

func hasLang(langs []string, l string) bool {
	for _, x := range langs {
		if x == l {
			return true
		}
	}
	return false
}

func computeTalk(in Inputs, brain bool) TalkCap {
	langs := append([]string{}, in.VoiceLangs...)
	out := TalkCap{TTSLanguage: langs}
	switch in.Whisper.State {
	case SvcMissingContent:
		out.State, out.Code = StateUnavailable, doctor.SVC010
		return out
	case SvcDown:
		out.State, out.Code = StateUnavailable, doctor.SVC011
		return out
	case SvcDisabled:
		out.State, out.Code, out.Reason = StateUnavailable, doctor.SVC010, ReasonDisabled
		return out
	}
	if !brain {
		out.State, out.Code, out.Reason = StateUnavailable, doctor.SVC106, ReasonNoBrain
		return out
	}
	out.STT = true
	switch {
	case in.Whisper.State == SvcStarting:
		out.State, out.Reason = StateDegraded, ReasonStarting
	case in.Whisper.State == SvcDegraded:
		out.State, out.Reason = StateDegraded, ReasonServiceDegraded
	case in.Voices == 0:
		out.State, out.Code, out.Reason = StateDegraded, doctor.SVC022, ReasonNoVoice
	case !hasLang(in.VoiceLangs, in.Lang):
		out.State, out.Code, out.Reason = StateDegraded, doctor.SVC021, ReasonNoVoiceLanguage
	default:
		out.State = StateReady
	}
	return out
}

func computeLibrary(in Inputs) LibraryCap {
	out := LibraryCap{Books: append([]Book{}, in.Books...)}
	switch {
	case in.Kiwix.State == SvcDown:
		out.State, out.Code = StateUnavailable, doctor.SVC002
	case in.Kiwix.State == SvcMissingContent, in.Kiwix.State == SvcDisabled, len(in.Books) == 0 && in.Kiwix.State != SvcStarting:
		out.State, out.Code, out.Reason = StateUnavailable, doctor.SVC001, ReasonNoBooks
	case in.Kiwix.State == SvcStarting:
		out.State, out.Reason = StateDegraded, ReasonStarting
	case in.Kiwix.State == SvcDegraded:
		out.State, out.Reason = StateDegraded, ReasonServiceDegraded
		if in.Kiwix.has(doctor.SVC004) {
			out.Code = doctor.SVC004
		}
	default:
		out.State = StateReady
	}
	return out
}

func computeMaps(in Inputs) MapsCap {
	out := MapsCap{}
	switch {
	case in.Maps.State == SvcMissingContent, in.Maps.State == SvcDisabled, !in.HasMap && in.Maps.State != SvcStarting:
		out.State, out.Code, out.Reason = StateUnavailable, doctor.SVC030, ReasonNoMap
		return out
	case in.Maps.State == SvcDown:
		out.State, out.Code = StateUnavailable, doctor.SVC034
		return out
	}
	noAssets, noSearch := in.Maps.has(doctor.SVC031), in.Maps.has(doctor.SVC032)
	out.Search = !noSearch
	switch {
	case in.Maps.State == SvcStarting:
		out.State, out.Reason = StateDegraded, ReasonStarting
	case noAssets:
		out.State, out.Code, out.Reason = StateDegraded, doctor.SVC031, ReasonNoAssets
	case noSearch:
		out.State, out.Code, out.Reason = StateDegraded, doctor.SVC032, ReasonNoSearch
	case in.Maps.State == SvcDegraded:
		out.State, out.Reason = StateDegraded, ReasonServiceDegraded
	default:
		out.State = StateReady
	}
	return out
}

func computeManuals(in Inputs) ManualsCap {
	out := ManualsCap{Items: append([]Manual{}, in.Manuals...)}
	switch {
	case len(in.Manuals) == 0:
		out.State, out.Reason = StateUnavailable, ReasonNoDocs
	case in.ManualsNoInventory:
		out.State, out.Code, out.Reason = StateDegraded, doctor.SYN062, ReasonNoInventory
	default:
		out.State = StateReady
	}
	return out
}

package portal

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

// HealthView is the companion services' supervised state; *health.Supervisor implements it. It
// returns cached values (the supervisor probes on its own schedule).
type HealthView interface {
	Records(ctx context.Context) []health.Record
}

// ArkSummary is the node-level readiness the status shows (survival-portal.md section 3.5).
type ArkSummary struct {
	// Overall is green, yellow or red; "" means no report.
	Overall         string
	SurvivalCapable bool
	DriveConnected  bool
	// ClockWrong is READY.json clock_wrong.
	ClockWrong bool
	// NewestVerifiedAt is the newest verified_at in any mounted root's READY.json; zero for none.
	NewestVerifiedAt time.Time
}

// ArkView supplies the ArkSummary from the readiness reports already held in memory.
type ArkView interface {
	ArkSummary() ArkSummary
}

// Doc is one rendered document of docs/index.json.
type Doc struct {
	Slug, Title, TitleES, HTML string
}

// DocsInfo is what a DocsView answers.
type DocsInfo struct {
	Docs []Doc
	// NoInventory is true when the documents were rendered without the lab inventory (LAI-SYN-062).
	NoInventory bool
}

// DocsView supplies the rendered manuals.
type DocsView interface {
	Docs() DocsInfo
}

// FirstAidView resolves portal_links for the status. *FirstAidResolver implements it; it must answer
// from a cache and never ask kiwix.
type FirstAidView interface {
	Resolve(cat *catalog.Catalog, kiwix health.Record, lang string) []FirstAid
}

// Sources are the read-only views the Builder assembles the body from. Every field is optional: a
// missing view renders its documented empty state, so the endpoint answers on a node whose services
// are down. No view runs a check.
type Sources struct {
	Config   statusapi.ConfigView
	State    statusapi.StateView
	Mode     statusapi.ModeView
	Hardware statusapi.HardwareView
	Router   statusapi.RouterView
	Catalog  *statusapi.CatalogStore
	Alerts   statusapi.AlertView
	// Models merges the catalog with local state: the brain choices and what is loadable.
	Models statusapi.ModelView
	// Health supplies kiwix, whisper, piper and maps; Llama supplies the router service.
	Health HealthView
	Llama  statusapi.ServiceView
	Ark    ArkView
	Docs   DocsView
	// FirstAid resolves the first_aid entries from its cached availability; nil leaves them empty.
	FirstAid FirstAidView
	// Settings resolves emergency_number; nil uses the defaults.
	Settings func() Settings
	// BuildTime is when the agent binary was built: a clock earlier than it is wrong (clock_ok false).
	// The zero time disables the check.
	BuildTime time.Time
}

// Builder builds the full body of GET /portal/status.
type Builder struct{ src Sources }

var _ StatusBuilder = (*Builder)(nil)

// NewBuilder returns the builder over src.
func NewBuilder(src Sources) *Builder { return &Builder{src: src} }

// Build implements StatusBuilder.
func (b *Builder) Build(ctx context.Context, req Request) (Status, error) {
	s := b.src
	lang := req.Lang
	if lang != doctor.LangES {
		lang = doctor.LangEN
	}
	cfg := config.Defaults()
	if s.Config != nil {
		cfg = s.Config.Get()
	}
	settings := Settings{EmergencyNumber: DefaultEmergencyNumber}
	if s.Settings != nil {
		settings = s.Settings()
	}
	if settings.EmergencyNumber == "" {
		settings.EmergencyNumber = DefaultEmergencyNumber
	}
	var snap hardware.Snapshot
	if s.Hardware != nil {
		snap = s.Hardware.Snapshot()
	}
	session := req.Session
	if session == "" {
		session = SessionNone
	}
	var cat *catalog.Catalog
	if s.Catalog != nil {
		cat = s.Catalog.Get().Catalog
	}
	var prof *catalog.Profile
	if cat != nil {
		prof, _ = cat.Profile(cfg.Profile)
	}

	brain := b.brain(cat)
	rows := b.modelRows(lang)
	brain.Choices = choices(cat, prof, rows)

	recs := map[string]health.Record{}
	if s.Health != nil {
		for _, r := range s.Health.Records(ctx) {
			recs[r.Name] = r
		}
	}
	var docs DocsInfo
	if s.Docs != nil {
		docs = s.Docs.Docs()
	}
	var ark ArkSummary
	if s.Ark != nil {
		ark = s.Ark.ArkSummary()
	}

	in := Inputs{
		Lang: lang, Loaded: len(brain.Loaded), Loading: brain.Loading != nil,
		Llama: b.llama(ctx), Whisper: svcOf(recs["whisper"]), Kiwix: svcOf(recs["kiwix"]), Maps: svcOf(recs["maps"]),
		Books: books(cat, recs["kiwix"]), HasMap: len(recs["maps"].Maps) > 0,
		VoiceLangs: voiceLangs(recs["piper"].Voices), Voices: len(recs["piper"].Voices),
		Manuals: manuals(docs.Docs, lang), ManualsNoInventory: docs.NoInventory,
	}
	for _, m := range brain.Loaded {
		in.LoadedVision = in.LoadedVision || m.Vision
	}
	for _, c := range brain.Choices {
		in.AnyLoadable = in.AnyLoadable || c.Loadable
	}
	in.VisionModel = visionModel(cat, prof, rows)

	power := powerOf(snap.Power)
	return Status{
		SchemaVersion: SchemaVersion, GeneratedAt: req.Now.UTC().Format(time.RFC3339),
		NodeID: cfg.Node.ID, Mode: b.mode(), Language: lang, EmergencyNumber: settings.EmergencyNumber,
		Session: session, Power: power, ClockOK: clockOK(req.Now, s.BuildTime, ark),
		Brain: brain, Capabilities: ComputeCapabilities(in), FirstAid: b.firstAid(cat, recs["kiwix"], lang),
		Ark:    Ark{Overall: orUnknown(ark.Overall), SurvivalCapable: ark.SurvivalCapable, DriveConnected: ark.DriveConnected},
		Alerts: b.alerts(lang, power, snap, cfg.Llama.Backend),
	}, nil
}

func (b *Builder) firstAid(cat *catalog.Catalog, kiwix health.Record, lang string) []FirstAid {
	if b.src.FirstAid == nil {
		return []FirstAid{}
	}
	out := b.src.FirstAid.Resolve(cat, kiwix, lang)
	if out == nil {
		return []FirstAid{}
	}
	return out
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// clockOK is false when the clock is earlier than the agent's build time or than the newest
// verified_at in READY.json (survival-portal.md section 3.5), or when READY.json says the clock is wrong.
func clockOK(now, build time.Time, ark ArkSummary) bool {
	if ark.ClockWrong {
		return false
	}
	if !build.IsZero() && now.Before(build) {
		return false
	}
	return ark.NewestVerifiedAt.IsZero() || !now.Before(ark.NewestVerifiedAt)
}

// mode is the committed mode: the engine's, else state.json's, else offline (the safe value).
func (b *Builder) mode() string {
	switch {
	case b.src.Mode != nil:
		return string(b.src.Mode.Info().Mode)
	case b.src.State != nil:
		return string(b.src.State.Snapshot().Mode)
	}
	return string(modes.Offline)
}

func powerOf(p hardware.Power) Power {
	out := Power{OnAC: p.Source == hardware.SourceAC, BatteryPct: p.BatteryPercent}
	if p.TimeRemainingS != nil {
		m := int(*p.TimeRemainingS / 60)
		out.MinutesLeft = &m
	}
	switch {
	case !p.HasBattery():
		out.State = PowerNoBattery
	case p.Source == hardware.SourceBattery:
		out.State = PowerDischarging
		out.Low = *p.BatteryPercent <= lowPct
		out.Critical = *p.BatteryPercent <= criticalPct
	case p.Charging:
		out.State = PowerCharging
	default:
		out.State = PowerFull
	}
	return out
}

// brain lists the loaded and the loading models the router reports and the catalog knows. A model
// that is not in the catalog (a LLAMA_CACHE model) is left out: the Portal offers only what the
// catalog describes.
func (b *Builder) brain(cat *catalog.Catalog) Brain {
	out := Brain{Loaded: []Model{}, Choices: []Choice{}}
	if b.src.Router == nil || cat == nil {
		return out
	}
	for _, ms := range b.src.Router.View().Models {
		cm, ok := cat.Model(ms.ID)
		if !ok {
			continue
		}
		m := modelOf(cm)
		switch {
		case ms.Failed:
		case ms.Status == router.StatusLoaded:
			out.Loaded = append(out.Loaded, m)
		case ms.Status == router.StatusLoading && out.Loading == nil:
			out.Loading = &m
		}
	}
	return out
}

func modelOf(cm *catalog.Model) Model {
	return Model{ID: cm.ID, DisplayName: cm.DisplayName, Roles: append([]string{}, cm.Roles...), Vision: hasVision(cm)}
}

func hasVision(cm *catalog.Model) bool {
	if cm.Llama != nil && cm.Llama.MmprojFileKind != "" {
		return true
	}
	for _, r := range cm.Roles {
		if r == "vision" {
			return true
		}
	}
	return false
}

func (b *Builder) modelRows(lang string) []statusapi.ModelRow {
	if b.src.Models == nil {
		return nil
	}
	return b.src.Models.Models(lang).Rows
}

// choices are the catalog llama models of the profile, survival ones first, otherwise in catalog order.
func choices(cat *catalog.Catalog, prof *catalog.Profile, rows []statusapi.ModelRow) []Choice {
	out := []Choice{}
	if cat == nil {
		return out
	}
	surv := map[string]bool{}
	if prof != nil {
		for _, id := range prof.Survival.Models {
			surv[id] = true
		}
	}
	for _, r := range rows {
		cm, ok := cat.Model(r.ID)
		if !ok || r.Engine != "llama" || !r.InProfile {
			continue
		}
		out = append(out, Choice{
			ID: r.ID, DisplayName: r.DisplayName, Summary: r.Summary, Roles: append([]string{}, r.Roles...),
			Vision: hasVision(cm), Loadable: r.Loadable, Survival: surv[r.ID],
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Survival && !out[j].Survival })
	return out
}

// visionModel answers "which model answers a photo when none loaded does" (companion-services.md
// section 4.5): the first loadable vision model of profile.survival.models, then of
// profile.include.models, then any loadable vision model in catalog order. "" means none.
func visionModel(cat *catalog.Catalog, prof *catalog.Profile, rows []statusapi.ModelRow) string {
	if cat == nil {
		return ""
	}
	ok := map[string]bool{}
	for _, r := range rows {
		if cm, found := cat.Model(r.ID); found && r.Loadable && r.Engine == "llama" && hasVision(cm) {
			ok[r.ID] = true
		}
	}
	var order []string
	if prof != nil {
		order = append(order, prof.Survival.Models...)
		order = append(order, prof.Include.Models...)
	}
	for _, r := range rows {
		order = append(order, r.ID)
	}
	for _, id := range order {
		if ok[id] {
			return id
		}
	}
	return ""
}

func (b *Builder) llama(ctx context.Context) Svc {
	if b.src.Llama == nil {
		return Svc{}
	}
	for _, st := range b.src.Llama.Snapshot(ctx) {
		if st.Name == "llama" {
			return Svc{State: st.Health}
		}
	}
	return Svc{}
}

func svcOf(r health.Record) Svc {
	out := Svc{State: string(r.State)}
	for _, p := range r.Problems {
		out.Problems = append(out.Problems, p.Code)
	}
	return out
}

func books(cat *catalog.Catalog, kiwix health.Record) []Book {
	out := []Book{}
	for _, bk := range kiwix.Books {
		b := Book{ContentID: bk.ContentID, Title: bk.Name, Book: bk.Name}
		if cat != nil {
			if it, ok := cat.ContentItem(bk.ContentID); ok {
				if it.DisplayName != "" {
					b.Title = it.DisplayName
				}
				if len(it.Languages) > 0 {
					b.Lang = it.Languages[0]
				}
			}
		}
		out = append(out, b)
	}
	return out
}

var langRe = regexp.MustCompile(`^[a-z]{2,3}$`)

// voiceLangs maps voice ids such as "en_US-name-medium" to their language codes, once each.
func voiceLangs(ids []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		l := id
		if i := strings.IndexAny(id, "_-"); i > 0 {
			l = id[:i]
		}
		l = strings.ToLower(l)
		// Only a two or three letter code is shown: anything else (a path, say) is not a language.
		if langRe.MatchString(l) && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

// manuals maps docs/index.json entries to {id, title (localized), url}. Only links under /docs/ pass.
func manuals(docs []Doc, lang string) []Manual {
	out := []Manual{}
	for _, d := range docs {
		if !strings.HasPrefix(d.HTML, "/docs/") || strings.Contains(d.HTML, "..") || d.Slug == "" {
			continue
		}
		title := d.Title
		if lang == doctor.LangES && d.TitleES != "" {
			title = d.TitleES
		}
		out = append(out, Manual{ID: d.Slug, Title: title, URL: d.HTML})
	}
	return out
}

// alerts are the active alerts in lang, plus the three the Portal derives from the live readings
// (battery low, battery very low, no graphics card) unless the alert manager already lists them.
func (b *Builder) alerts(lang string, p Power, snap hardware.Snapshot, backend string) []Alert {
	out := []Alert{}
	have := map[string]bool{}
	if b.src.Alerts != nil {
		for _, a := range b.src.Alerts.Active() {
			a = a.In(lang)
			have[a.Code] = true
			out = append(out, Alert{Code: a.Code, Severity: string(a.Severity), Title: a.Title, Fix: a.Fix})
		}
	}
	add := func(code string) {
		if have[code] {
			return
		}
		c, ok := doctor.Lookup(code)
		if !ok {
			return
		}
		title, fix, _ := doctor.Text(code, lang)
		out = append(out, Alert{Code: code, Severity: string(c.Severity), Title: title, Fix: fix})
	}
	switch {
	case p.Critical:
		add(doctor.SVC112)
	case p.Low:
		add(doctor.SVC111)
	}
	if backend == hardware.BackendCPU {
		add(doctor.SVC113)
	}
	return out
}

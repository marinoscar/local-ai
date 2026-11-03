package plan

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// Column minimums of the human plan (the widths of the spec's example, ark-sync.md section 4.4.4).
// A column grows to its longest cell, so a long id never breaks the alignment; the gaps are fixed.
const (
	colItem    = 27
	colTier    = 4
	colAction  = 8
	colVersion = 20
	colSize    = 8
	maxListed  = 6 // ids listed after a finding before "and N more"
)

// words is the text of the human plan in one language.
type words struct {
	planFor, budget, planned, freeNow, keepFree                   string
	cItem, cTier, cAction, cVersion, cSize, cNote                 string
	downloads, peak, dryRun, notes, fix, notForNode, offline      string
	readOnly, keepRefresh, keepNoInterval, keepUnresolved         string
	keepUnresolvedOffline, keepBudget, refreshDue, catalogPin     string
	was, oldKept, fromLocal, localWaits, frees, budgetNote        string
	diskNote, unresolved, unresolvedOffline, readOnlyNote, orphan string
	itemOne, itemMany, andMore, sevInfo, sevWarning, sevError     string
	actions                                                       map[Action]string
}

var en = words{
	planFor: `Plan for root %q%s · profile %s · catalog %s`,
	budget:  "Budget", planned: "planned", freeNow: "free now", keepFree: "keep free ≥",
	cItem: "ITEM", cTier: "TIER", cAction: "ACTION", cVersion: "VERSION", cSize: "SIZE", cNote: "NOTE",
	downloads: "Downloads: %d %s, %s from the internet, %s local copy.",
	peak:      "Peak disk use during sync: %s (free will not drop below %s).",
	dryRun:    "Nothing was changed (dry run).",
	notes:     "Notes:", fix: "Fix:",
	notForNode:  "Not for this node (left out): %s",
	offline:     "No network is used (offline or survival mode): items that need it are shown as unresolved.",
	readOnly:    "This root is read-only: nothing will be written to it.",
	keepRefresh: "newer version available; refresh in %d d", keepNoInterval: "newer version available; no refresh interval",
	keepUnresolved: "kept; newest version unknown", keepUnresolvedOffline: "kept; newest version unknown (offline)",
	keepBudget: "update skipped: budget",
	refreshDue: "refresh due (%d d)", catalogPin: "catalog pin changed", was: "was %s", oldKept: "old copy kept in .trash",
	fromLocal: "from %s (local copy)", localWaits: "from %s (local copy, after it finishes)",
	frees:      "frees %s of old versions in .trash",
	budgetNote: "budget", diskNote: "not enough free disk space", unresolved: "unresolved", unresolvedOffline: "unresolved (offline)",
	readOnlyNote: "read-only root", orphan: "not in profile; run `aictl ark prune --orphans`",
	itemOne: "item", itemMany: "items", andMore: "and %d more",
	actions: map[Action]string{ActionKeep: "keep", ActionDownload: "download", ActionUpdate: "update", ActionSkip: "skip", ActionOrphan: "orphan"},
	sevInfo: "info", sevWarning: "warning", sevError: "error",
}

var es = words{
	planFor: `Plan para la raíz %q%s · perfil %s · catálogo %s`,
	budget:  "Presupuesto", planned: "planificado", freeNow: "libre ahora", keepFree: "mantener libre ≥",
	cItem: "ELEMENTO", cTier: "NIVEL", cAction: "ACCIÓN", cVersion: "VERSIÓN", cSize: "TAMAÑO", cNote: "NOTA",
	downloads: "Descargas: %d %s, %s de internet, %s de copia local.",
	peak:      "Uso máximo de disco durante la sincronización: %s (el espacio libre no bajará de %s).",
	dryRun:    "No se cambió nada (simulacro).",
	notes:     "Avisos:", fix: "Solución:",
	notForNode:  "No son para este equipo (omitidos): %s",
	offline:     "No se usa la red (modo sin conexión o supervivencia): los elementos que la necesitan aparecen sin resolver.",
	readOnly:    "Esta raíz es de solo lectura: no se escribirá nada en ella.",
	keepRefresh: "hay una versión más nueva; se actualizará en %d d", keepNoInterval: "hay una versión más nueva; sin intervalo de actualización",
	keepUnresolved: "se conserva; versión más reciente desconocida", keepUnresolvedOffline: "se conserva; versión más reciente desconocida (sin conexión)",
	keepBudget: "actualización omitida: presupuesto",
	refreshDue: "toca actualizar (%d d)", catalogPin: "cambió la versión fijada en el catálogo", was: "era %s", oldKept: "la copia anterior queda en .trash",
	fromLocal: "desde %s (copia local)", localWaits: "desde %s (copia local, cuando termine)",
	frees:      "libera %s de versiones antiguas en .trash",
	budgetNote: "presupuesto", diskNote: "no hay espacio libre suficiente en el disco", unresolved: "sin resolver", unresolvedOffline: "sin resolver (sin conexión)",
	readOnlyNote: "raíz de solo lectura", orphan: "no está en el perfil; ejecute `aictl ark prune --orphans`",
	itemOne: "elemento", itemMany: "elementos", andMore: "y %d más",
	actions: map[Action]string{ActionKeep: "conservar", ActionDownload: "descargar", ActionUpdate: "actualizar", ActionSkip: "omitir", ActionOrphan: "huérfano"},
	sevInfo: "info", sevWarning: "aviso", sevError: "error",
}

func wordsFor(lang string) words {
	if lang == doctor.LangES {
		return es
	}
	return en
}

// FormatGB prints bytes as gigabytes of 10^9 bytes with one decimal, rounding half up ("48.7 GB"). It
// uses integer arithmetic, so the text never depends on floating-point rounding.
func FormatGB(b int64) string {
	if b < 0 {
		b = 0
	}
	tenths := (b + 50_000_000) / 100_000_000
	return strconv.FormatInt(tenths/10, 10) + "." + strconv.FormatInt(tenths%10, 10) + " GB"
}

func runes(s string) int { return utf8.RuneCountInString(s) }

func pad(s string, w int) string {
	if n := runes(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

func padLeft(s string, w int) string {
	if n := runes(s); n < w {
		return strings.Repeat(" ", w-n) + s
	}
	return s
}

// Render is the human text of a plan (the golden-file form of ark-sync.md section 4.4.4) in "en" or
// "es". It is a pure function of the plan, so the same plan always prints the same bytes, and it
// works from a plan decoded from JSON, which is what `aictl ark sync --dry-run` does.
func Render(p RootPlan, lang string) string {
	w := wordsFor(lang)
	var b strings.Builder
	line := func(format string, args ...any) {
		b.WriteString(strings.TrimRight(fmt.Sprintf(format, args...), " "))
		b.WriteByte('\n')
	}

	path := ""
	if p.Path != "" {
		path = " (" + p.Path + ")"
	}
	line(w.planFor, p.Root, path, p.Profile, p.CatalogVersion)
	line("%s %s · %s %s · %s %s · %s %s", w.budget, FormatGB(p.BudgetBytes), w.planned, FormatGB(p.PlannedBytes),
		w.freeNow, FormatGB(p.FSFreeBytes), w.keepFree, FormatGB(p.MinFreeBytes))
	line("")

	type row struct{ pos, id, tier, action, version, size, note string }
	rows := make([]row, len(p.Items))
	itemW, tierW, actionW, versionW, sizeW := colItem, max(colTier, runes(w.cTier)), max(colAction, runes(w.cAction)), colVersion, max(colSize, runes(w.cSize))
	for i, it := range p.Items {
		r := row{pos: "--", id: it.ID, tier: "-", action: w.actions[it.Action], version: displayVersion(it), size: "-", note: note(p, it, w)}
		if it.Pos > 0 {
			r.pos = strconv.Itoa(it.Pos)
		}
		if it.Tier != nil {
			r.tier = it.Tier.String()
		}
		if it.FinalBytes > 0 {
			r.size = FormatGB(it.FinalBytes)
		}
		rows[i] = r
		itemW, tierW = max(itemW, runes(r.id)), max(tierW, runes(r.tier))
		actionW, versionW, sizeW = max(actionW, runes(r.action)), max(versionW, runes(r.version)), max(sizeW, runes(r.size))
	}
	itemW, versionW = max(itemW, runes(w.cItem)), max(versionW, runes(w.cVersion))
	// The gaps are two spaces, except one between VERSION and SIZE (the spec's example).
	cells := func(pos, id, tier, action, version, size, note string) {
		line("%s  %s  %s  %s  %s %s  %s", padLeft(pos, 3), pad(id, itemW), pad(tier, tierW), pad(action, actionW),
			pad(version, versionW), pad(size, sizeW), note)
	}
	cells("#", w.cItem, w.cTier, w.cAction, w.cVersion, w.cSize, w.cNote)
	for _, r := range rows {
		cells(r.pos, r.id, r.tier, r.action, r.version, r.size, r.note)
	}
	line("")

	// Notes: the situation, then every doctor code the plan raises with its one-sentence fix.
	var notes []string
	if p.Offline {
		notes = append(notes, w.offline)
	}
	if p.ReadOnly {
		notes = append(notes, w.readOnly)
	}
	if len(p.NotForNode) > 0 {
		notes = append(notes, fmt.Sprintf(w.notForNode, listIDs(p.NotForNode, w)))
	}
	for _, f := range p.Findings {
		title, fix, _ := doctor.Text(f.Code, lang)
		text := fmt.Sprintf("[%s] %s  %s", severityWord(f, w), f.Code, title)
		if len(f.Items) > 0 {
			text += ": " + listIDs(f.Items, w)
		}
		notes = append(notes, text)
		if fix != "" {
			notes = append(notes, "    "+w.fix+" "+fix)
		}
	}
	if len(notes) > 0 {
		line("%s", w.notes)
		for _, n := range notes {
			if strings.HasPrefix(n, "    ") {
				line("%s", n)
			} else {
				line("  %s", n)
			}
		}
		line("")
	}

	t := p.Totals()
	noun := w.itemMany
	if t.Items == 1 {
		noun = w.itemOne
	}
	line(w.downloads, t.Items, noun, FormatGB(t.NetworkBytes), FormatGB(t.LocalBytes))
	line(w.peak, FormatGB(p.PeakBytes), FormatGB(p.FreeFloorBytes))
	line("%s", w.dryRun)
	return b.String()
}

// RenderAll is Render for every root of a dry run, separated by a blank line.
func RenderAll(plans []RootPlan, lang string) string {
	parts := make([]string, len(plans))
	for i, p := range plans {
		parts[i] = Render(p, lang)
	}
	return strings.Join(parts, "\n")
}

func severityWord(f Finding, w words) string {
	switch f.Severity {
	case doctor.Error:
		return w.sevError
	case doctor.Warning:
		return w.sevWarning
	}
	return w.sevInfo
}

func listIDs(ids []string, w words) string {
	if len(ids) <= maxListed {
		return strings.Join(ids, ", ")
	}
	return strings.Join(ids[:maxListed], ", ") + " " + fmt.Sprintf(w.andMore, len(ids)-maxListed)
}

// displayVersion is what the VERSION column shows: what is kept for a keep and an orphan, otherwise
// the target version, else the present one, else "-".
func displayVersion(it ItemPlan) string {
	v := ""
	switch it.Action {
	case ActionKeep, ActionOrphan:
		v = it.FromVersion
	default:
		v = it.ToVersion
		if v == "" {
			v = it.FromVersion
		}
	}
	if v == "" {
		return "-"
	}
	return v
}

// note is the NOTE column: the reason in plain words.
func note(p RootPlan, it ItemPlan, w words) string {
	var parts []string
	switch it.Action {
	case ActionOrphan:
		return w.orphan
	case ActionKeep:
		switch it.Reason {
		case ReasonRefreshDays:
			if it.RefreshDays > 0 {
				parts = append(parts, fmt.Sprintf(w.keepRefresh, max(it.RefreshDays-it.AgeDays, 0)))
			} else {
				parts = append(parts, w.keepNoInterval)
			}
		case ReasonUnresolved:
			if p.Offline {
				parts = append(parts, w.keepUnresolvedOffline)
			} else {
				parts = append(parts, w.keepUnresolved)
			}
		case ReasonBudget:
			parts = append(parts, w.keepBudget)
		}
	case ActionSkip:
		switch it.Reason {
		case ReasonBudget:
			parts = append(parts, w.budgetNote)
		case ReasonDisk:
			parts = append(parts, w.diskNote)
		case ReasonUnresolved:
			if p.Offline {
				parts = append(parts, w.unresolvedOffline)
			} else {
				parts = append(parts, w.unresolved)
			}
		case ReasonReadOnly:
			parts = append(parts, w.readOnlyNote)
		default:
			parts = append(parts, string(it.Reason))
		}
	case ActionDownload, ActionUpdate:
		if it.Action == ActionUpdate {
			switch it.Reason {
			case ReasonRefreshDue:
				parts = append(parts, fmt.Sprintf(w.refreshDue, it.AgeDays))
			case ReasonCatalogPin:
				parts = append(parts, w.catalogPin)
			}
			parts = append(parts, fmt.Sprintf(w.was, it.FromVersion), w.oldKept)
		}
		if it.Source == SourceLocalCopy {
			if it.After != "" {
				parts = append(parts, fmt.Sprintf(w.localWaits, it.FromRoot))
			} else {
				parts = append(parts, fmt.Sprintf(w.fromLocal, it.FromRoot))
			}
		}
		if it.PurgeBytes > 0 {
			parts = append(parts, fmt.Sprintf(w.frees, FormatGB(it.PurgeBytes)))
		}
	}
	return strings.Join(parts, "; ")
}

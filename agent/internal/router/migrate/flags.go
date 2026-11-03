package migrate

import (
	"fmt"
	"path"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
)

// ExecFlags is the llama-server command line of the legacy unit, reduced to what the migration reads
// (llama-router.md section 4.9.2 step M0 item 1).
type ExecFlags struct {
	// Binary is the first word of ExecStart.
	Binary string
	// Model, Draft and Mmproj are the paths of -m, -md and --mmproj ("" when absent).
	Model, Draft, Mmproj string
	// Aliases are the --alias names (a comma list is split).
	Aliases []string
	// Ctx is -c (0 when absent); NGL is -ngl as written ("all" or a number; "" when absent).
	Ctx int
	NGL string
	// FlashAttn is on, off or auto; "" when the flag is absent.
	FlashAttn string
	// Parallel is --parallel (0 when absent).
	Parallel int
	Host     string
	Port     int
	// APIKeyFile is --api-key-file as written (it may be %d/<credential>).
	APIKeyFile string
	// SpecType, SpecNMax and SpecDraftNGL are the --spec-* flags the catalog knows.
	SpecType     string
	SpecNMax     int
	SpecNMaxSet  bool
	SpecDraftNGL string
	// Extra holds every other flag as written, normalized to "--flag value" or "--flag" (see
	// normalizeExtra), in order.
	Extra []string
	// InlineKey is true when the unit passes the key on the command line (--api-key): the value is
	// never kept.
	InlineKey bool
}

// ParseExec reduces the ExecStart words to ExecFlags. It refuses what the migration cannot carry over
// faithfully: a command that is not llama-server itself, an unreadable number, an environment variable
// reference ($NAME), a missing value.
func ParseExec(argv []string) (*ExecFlags, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("%w: ExecStart= is empty", ErrUnitSyntax)
	}
	f := &ExecFlags{Binary: argv[0]}
	if path.Base(f.Binary) != "llama-server" {
		return nil, fmt.Errorf("%w: ExecStart= runs %q, not llama-server", ErrUnitSyntax, path.Base(f.Binary))
	}
	args := argv[1:]
	for _, a := range args {
		if strings.Contains(a, "$") {
			return nil, fmt.Errorf("%w: the argument %q uses an environment reference the migration cannot expand", ErrUnitSyntax, clip(a, 40))
		}
	}
	for i := 0; i < len(args); i++ {
		tok := args[i]
		if !strings.HasPrefix(tok, "-") || tok == "-" {
			return nil, fmt.Errorf("%w: the unexpected word %q in ExecStart=", ErrUnitSyntax, clip(tok, 40))
		}
		name, val, hasVal := tok, "", false
		if strings.HasPrefix(tok, "--") {
			if n, v, ok := strings.Cut(tok, "="); ok {
				name, val, hasVal = n, v, true
			}
		}
		// next returns the value of the flag: the inline one, else the next word.
		next := func() (string, error) {
			if hasVal {
				return val, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("%w: %s has no value", ErrUnitSyntax, name)
			}
			i++
			return args[i], nil
		}
		var err error
		var v string
		switch name {
		case "-m", "--model":
			f.Model, err = next()
		case "-md", "--model-draft":
			f.Draft, err = next()
		case "--mmproj":
			f.Mmproj, err = next()
		case "-a", "--alias":
			if v, err = next(); err == nil {
				for _, a := range strings.Split(v, ",") {
					if a = strings.TrimSpace(a); a != "" {
						f.Aliases = append(f.Aliases, a)
					}
				}
			}
		case "-c", "--ctx-size":
			if v, err = next(); err == nil {
				n, ok := atoi(v)
				if !ok {
					return nil, fmt.Errorf("%w: %s %q is not a number", ErrUnitSyntax, name, clip(v, 20))
				}
				f.Ctx = n
			}
		case "-ngl", "--n-gpu-layers", "--gpu-layers":
			f.NGL, err = next()
		case "-fa", "--flash-attn":
			// The value is optional in old builds (a bare -fa means on).
			switch {
			case hasVal:
				f.FlashAttn = val
			case i+1 < len(args) && isFlashValue(args[i+1]):
				i++
				f.FlashAttn = args[i]
			default:
				f.FlashAttn = "on"
			}
		case "-np", "--parallel":
			if v, err = next(); err == nil {
				n, ok := atoi(v)
				if !ok {
					return nil, fmt.Errorf("%w: %s %q is not a number", ErrUnitSyntax, name, clip(v, 20))
				}
				f.Parallel = n
			}
		case "--host":
			f.Host, err = next()
		case "--port":
			if v, err = next(); err == nil {
				n, ok := atoi(v)
				if !ok {
					return nil, fmt.Errorf("%w: --port %q is not a number", ErrUnitSyntax, clip(v, 20))
				}
				f.Port = n
			}
		case "--api-key-file":
			f.APIKeyFile, err = next()
		case "--api-key":
			// The key itself: never read into the plan, never printed.
			f.InlineKey = true
			_, err = next()
		case "--spec-type":
			f.SpecType, err = next()
		case "--spec-draft-n-max":
			if v, err = next(); err == nil {
				n, ok := atoi(v)
				if !ok {
					return nil, fmt.Errorf("%w: --spec-draft-n-max %q is not a number", ErrUnitSyntax, clip(v, 20))
				}
				f.SpecNMax, f.SpecNMaxSet = n, true
			}
		case "--spec-draft-ngl":
			f.SpecDraftNGL, err = next()
		default:
			// Any other flag: a value follows when the next word is not a flag (the rule the preset
			// renderer uses for extra_args too).
			ex := name
			switch {
			case hasVal:
				ex = name + " " + val
			case i+1 < len(args) && !looksLikeFlag(args[i+1]):
				i++
				ex = name + " " + args[i]
			}
			f.Extra = append(f.Extra, ex)
		}
		if err != nil {
			return nil, err
		}
	}
	return f, nil
}

func isFlashValue(s string) bool { return s == "on" || s == "off" || s == "auto" }

// looksLikeFlag is true for a word that starts a new flag; a negative number is a value.
func looksLikeFlag(s string) bool {
	if !strings.HasPrefix(s, "-") || len(s) == 1 {
		return false
	}
	if _, ok := atoi(strings.TrimPrefix(s, "-")); ok {
		return false
	}
	return true
}

// normalizeExtra turns a catalog extra_args list into the "--flag value" form ParseExec produces:
// an entry "--foo bar", "--foo=bar" and the split form ["--foo", "bar"] all mean the same.
func normalizeExtra(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			if len(out) > 0 {
				out[len(out)-1] += " " + a // the value of the previous flag
			}
			continue
		}
		if k, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(a, "--") {
			a = k + " " + v
		}
		out = append(out, a)
	}
	return out
}

// FlagDiff is one difference between the legacy unit and the catalog's llama block.
type FlagDiff struct {
	// Flag is the llama-server flag ("-c", "-ngl", "--spec-type", ...).
	Flag string `json:"flag"`
	// Unit and Catalog are the two values as text; "(unset)" when one side has none.
	Unit    string `json:"unit"`
	Catalog string `json:"catalog"`
}

func (d FlagDiff) String() string {
	return fmt.Sprintf("%s: the unit has %s, the catalog has %s", d.Flag, d.Unit, d.Catalog)
}

const unset = "(unset)"

// DiffCatalog compares the legacy flags with the matched catalog model's llama block (spec M0 item 4:
// ctx, ngl, flash-attn, parallel, speculative type and n_max, the draft file, and the other flags). The
// kinds say which catalog file kind each of -md and --mmproj matched. A flag the unit lacks counts as the
// llama-server default: flash-attn auto, parallel automatic, no speculation.
func DiffCatalog(f *ExecFlags, m *catalog.Model, draftKind, mmprojKind string) []FlagDiff {
	var out []FlagDiff
	add := func(flag, unit, cat string) { out = append(out, FlagDiff{Flag: flag, Unit: unit, Catalog: cat}) }
	l := m.Llama
	if l == nil {
		l = &catalog.Llama{}
	}
	// context
	if f.Ctx != l.Ctx {
		add("-c", numOrUnset(f.Ctx), numOrUnset(l.Ctx))
	}
	// gpu layers
	if !sameNGLText(f.NGL, l.NGPULayers) {
		add("-ngl", textOrUnset(f.NGL), nglText(l.NGPULayers))
	}
	// flash attention
	if fa, cf := orDefault(f.FlashAttn, "auto"), orDefault(l.FlashAttn, "auto"); fa != cf {
		add("--flash-attn", textOrUnset(f.FlashAttn), textOrUnset(l.FlashAttn))
	}
	// parallel slots
	if f.Parallel != l.Parallel {
		add("--parallel", numOrUnset(f.Parallel), numOrUnset(l.Parallel))
	}
	// speculative decoding
	var cs catalog.Speculative
	if l.Speculative != nil {
		cs = *l.Speculative
	}
	if f.SpecType != cs.Type {
		add("--spec-type", textOrUnset(f.SpecType), textOrUnset(cs.Type))
	}
	if f.SpecNMax != cs.NMax {
		add("--spec-draft-n-max", numOrUnset(f.SpecNMax), numOrUnset(cs.NMax))
	}
	if (f.Draft != "") != (cs.DraftFileKind != "") || (f.Draft != "" && draftKind != cs.DraftFileKind) {
		add("-md", pathOrUnset(f.Draft), textOrUnset(cs.DraftFileKind))
	}
	if f.Draft != "" && !sameNGLText(f.SpecDraftNGL, cs.DraftNGL) {
		add("--spec-draft-ngl", textOrUnset(f.SpecDraftNGL), nglText(cs.DraftNGL))
	}
	if (f.Mmproj != "") != (l.MmprojFileKind != "") || (f.Mmproj != "" && mmprojKind != l.MmprojFileKind) {
		add("--mmproj", pathOrUnset(f.Mmproj), textOrUnset(l.MmprojFileKind))
	}
	// every other flag
	have, want := map[string]bool{}, map[string]bool{}
	for _, e := range f.Extra {
		have[e] = true
	}
	cat := normalizeExtra(l.ExtraArgs)
	for _, e := range cat {
		want[e] = true
	}
	for _, e := range f.Extra {
		if !want[e] {
			add(strings.Fields(e)[0], e, unset)
		}
	}
	for _, e := range cat {
		if !have[e] {
			add(strings.Fields(e)[0], unset, e)
		}
	}
	return out
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func numOrUnset(n int) string {
	if n == 0 {
		return unset
	}
	return fmt.Sprint(n)
}

func textOrUnset(s string) string {
	if s == "" {
		return unset
	}
	return s
}

func pathOrUnset(p string) string {
	if p == "" {
		return unset
	}
	return path.Base(p)
}

func nglText(v catalog.IntOrAll) string {
	switch {
	case !v.Set:
		return unset
	case v.All:
		return "all"
	}
	return fmt.Sprint(v.N)
}

// allLayers is the -ngl value from which llama-server offloads every layer of any model we serve (99 is
// what the legacy units use for "all").
const allLayers = 99

// sameNGLText compares a unit's -ngl text with the catalog value; "all" and a number of at least 99
// mean the same.
func sameNGLText(unitVal string, cat catalog.IntOrAll) bool {
	switch {
	case unitVal == "" && !cat.Set:
		return true
	case unitVal == "" || !cat.Set:
		return false
	}
	uAll := unitVal == "all" || unitVal == "-1"
	un, uok := atoi(unitVal)
	if uok && un >= allLayers {
		uAll = true
	}
	cAll := cat.All || cat.N >= allLayers
	if uAll || cAll {
		return uAll == cAll
	}
	return uok && un == cat.N
}

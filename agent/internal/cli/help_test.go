package cli

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// The help listing and the command tree against docs/specs/aictl.md (AICTL-10): `aictl help` lists
// exactly the public commands of sections 4.1 to 4.27a, none of section 4.28, with their marks, in
// English and Spanish; and the spec and the registry cannot drift apart without a test failing.

// specCommand is one command the spec documents.
type specCommand struct {
	path  string // "ark drives set-profile"
	marks string // "[local] [root]" as written in the heading or table row, "" when none
	line  int
	src   string // "heading", "table" or "synopsis"
}

var (
	specSpanRe     = regexp.MustCompile("`aictl ([^`]*)`")
	specMarkRe     = regexp.MustCompile(`\[(local|root|admin)\]`)
	specWordRe     = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	specAltWordRe  = regexp.MustCompile(`^[a-z][a-z0-9-]*(\|[a-z][a-z0-9-]*)+$`)
	specMarkCellRe = regexp.MustCompile(`^\s*(\[(local|root|admin)\]\s*)+$`)
)

// specPaths turns the text of a span ("ark drives set-profile <root-id> <profile>") into the command
// paths it names: the leading plain words, one path per alternative for "start|stop|restart".
func specPaths(text string, alternatives bool) []string {
	var words []string
	for _, tok := range strings.Fields(text) {
		if specWordRe.MatchString(tok) {
			words = append(words, tok)
			continue
		}
		if alternatives && specAltWordRe.MatchString(tok) {
			var out []string
			for _, alt := range strings.Split(tok, "|") {
				out = append(out, strings.Join(append(append([]string(nil), words...), alt), " "))
			}
			return out
		}
		break
	}
	if len(words) == 0 {
		return nil
	}
	return []string{strings.Join(words, " ")}
}

// splitRow splits a Markdown table row on the bars that are not escaped.
func splitRow(row string) []string {
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(row); i++ {
		switch {
		case row[i] == '\\' && i+1 < len(row) && row[i+1] == '|':
			cur.WriteString(`\|`)
			i++
		case row[i] == '|':
			cells = append(cells, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(row[i])
		}
	}
	return append(cells, cur.String())
}

// readSpecCommands reads the commands section 4 of aictl.md documents: the headings, the first cell of
// the tables, and the synopsis lines of the code blocks (lines that start with "aictl "), from "## 4.
// Commands" up to section 4.28 (the internal commands, which are not public).
func readSpecCommands(t *testing.T) []specCommand {
	t.Helper()
	b, err := os.ReadFile("../../../docs/specs/aictl.md")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	start, end := -1, -1
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "## 4. Commands"):
			start = i
		case strings.HasPrefix(l, "### 4.28 "):
			end = i
		}
	}
	if start < 0 || end < start {
		t.Fatal("section 4 of aictl.md not found")
	}
	var out []specCommand
	fenced := false
	heading := regexp.MustCompile(`^#{3,4} 4\.`)
	for i := start; i < end; i++ {
		l := lines[i]
		switch {
		case strings.HasPrefix(l, "```"):
			fenced = !fenced
		case heading.MatchString(l):
			for _, m := range specSpanRe.FindAllStringSubmatchIndex(l, -1) {
				for _, p := range specPaths(l[m[2]:m[3]], false) {
					out = append(out, specCommand{p, strings.Join(specMarkRe.FindAllString(l[m[1]:], -1), " "), i + 1, "heading"})
				}
			}
		case !fenced && strings.HasPrefix(l, "|"):
			cells := splitRow(l)
			if len(cells) < 3 {
				continue
			}
			first := cells[1]
			marks := ""
			if len(cells) > 2 && specMarkCellRe.MatchString(cells[2]) {
				marks = strings.TrimSpace(cells[2])
			}
			for _, m := range specSpanRe.FindAllStringSubmatchIndex(first, -1) {
				own := strings.Join(specMarkRe.FindAllString(first[m[1]:], -1), " ")
				for _, p := range specPaths(first[m[2]:m[3]], false) {
					out = append(out, specCommand{p, strings.TrimSpace(own + " " + marks), i + 1, "table"})
				}
			}
		case fenced && strings.HasPrefix(l, "aictl "):
			for _, p := range specPaths(strings.TrimPrefix(l, "aictl "), true) {
				out = append(out, specCommand{p, "", i + 1, "synopsis"})
			}
		}
	}
	return out
}

// registeredCommands lists every path of the command tree with its command.
func registeredCommands() map[string]*Command {
	applyLate(DefaultRegistry)
	all := map[string]*Command{}
	var walk func(c *Command, path []string)
	walk = func(c *Command, path []string) {
		all[strings.Join(path, " ")] = c
		for _, s := range c.Sub {
			walk(s, append(append([]string(nil), path...), s.Name))
		}
	}
	for _, c := range DefaultRegistry.Commands() {
		walk(c, []string{c.Name})
	}
	return all
}

// documentedButNotBuilt are commands the spec documents that another issue owns and that are not in
// the tree yet. Each entry says whose it is; the test fails when one is registered (the entry must go)
// so the list only ever shrinks.
var documentedButNotBuilt = map[string]string{}

func TestEveryDocumentedCommandIsRegisteredAndTheOtherWayRound(t *testing.T) {
	reg := registeredCommands()
	spec := readSpecCommands(t)
	documented := map[string]bool{}
	prefix := map[string]bool{}
	for _, s := range spec {
		documented[s.path] = true
		words := strings.Fields(s.path)
		for i := 1; i < len(words); i++ {
			prefix[strings.Join(words[:i], " ")] = true
		}
	}
	if len(documented) < 50 {
		t.Fatalf("only %d commands were read from aictl.md section 4: the reader is broken", len(documented))
	}

	for _, s := range spec {
		_, built := reg[s.path]
		reason, pending := documentedButNotBuilt[s.path]
		switch {
		case !built && !pending:
			t.Errorf("aictl.md line %d documents `aictl %s` (%s) but it is not registered: build it, or list it in documentedButNotBuilt with its owner", s.line, s.path, s.src)
		case built && pending:
			t.Errorf("`aictl %s` is registered: remove it from documentedButNotBuilt (%s)", s.path, reason)
		}
	}
	for path := range documentedButNotBuilt {
		if !documented[path] {
			t.Errorf("documentedButNotBuilt names `aictl %s`, which the spec does not document", path)
		}
	}
	for path, c := range reg {
		switch {
		case documented[path]:
		case len(c.Sub) > 0 && prefix[path]:
			// a group whose subcommands are documented
		default:
			t.Errorf("`aictl %s` is registered but aictl.md section 4 does not document it: document it, or remove it", path)
		}
	}
}

// The marks of a command that has no subcommands are the ones the spec writes next to it.
func TestMarksInTheCodeAreTheMarksOfTheSpec(t *testing.T) {
	reg := registeredCommands()
	seen := map[string]bool{}
	for _, s := range readSpecCommands(t) {
		c, ok := reg[s.path]
		if !ok || len(c.Sub) > 0 || s.src == "synopsis" {
			continue
		}
		seen[s.path] = true
		if got := c.Marks.String(); got != s.marks {
			t.Errorf("`aictl %s`: aictl.md line %d writes %q, the command carries %q", s.path, s.line, s.marks, got)
		}
	}
	if len(seen) < 40 {
		t.Errorf("only %d commands had their marks compared", len(seen))
	}
}

// ---- the listing -----------------------------------------------------------------------------------

var helpRowRe = regexp.MustCompile(`^  ([a-z][a-z0-9-]*(?: [a-z][a-z0-9-]*)*)  `)

// listedCommands parses the "Commands:" rows of `aictl help`: path -> the whole row.
func listedCommands(t *testing.T, helpText string) map[string]string {
	t.Helper()
	rows := map[string]string{}
	in := false
	for _, l := range strings.Split(helpText, "\n") {
		switch {
		case l == "Commands:" || l == "Comandos:":
			in = true
		case in && l == "":
			return rows
		case in:
			m := helpRowRe.FindStringSubmatch(l)
			if m == nil {
				t.Fatalf("a row of the command list cannot be read: %q", l)
			}
			rows[m[1]] = l
		}
	}
	t.Fatal("no command list in the help text")
	return nil
}

func TestHelpListsExactlyThePublicCommands(t *testing.T) {
	reg := registeredCommands()
	for _, lang := range []string{"en", "es"} {
		t.Run(lang, func(t *testing.T) {
			r := runCLI(t, DefaultRegistry, runOpts{env: map[string]string{"LANG": "C"}}, "--lang", lang, "help")
			if r.code != 0 || r.errOut != "" {
				t.Fatalf("exit %d\n%s", r.code, r.errOut)
			}
			rows := listedCommands(t, r.out)
			var listed, registered []string
			for p := range rows {
				listed = append(listed, p)
			}
			for p := range reg {
				registered = append(registered, p)
			}
			sort.Strings(listed)
			sort.Strings(registered)
			if strings.Join(listed, ",") != strings.Join(registered, ",") {
				t.Errorf("help lists\n%v\nbut the tree has\n%v", listed, registered)
			}
			// Every command the spec documents and the tree has is in the list, with its marks.
			for _, s := range readSpecCommands(t) {
				if _, built := reg[s.path]; !built {
					continue
				}
				if _, ok := rows[s.path]; !ok {
					t.Errorf("`aictl %s` is documented but not listed", s.path)
				}
			}
			for path, c := range reg {
				row := rows[path]
				if m := c.Marks.String(); m != "" && !strings.Contains(row, m) {
					t.Errorf("the row of %s lacks its marks %q: %q", path, m, row)
				}
				if c.Marks.String() == "" && strings.Contains(row, "[") {
					t.Errorf("the row of %s shows marks it does not have: %q", path, row)
				}
			}
			// Not the internal commands of aictl.md section 4.28.
			low := strings.ToLower(r.out)
			for _, banned := range []string{"aiagent", "update-rollback-check", "migrate", "llama-qwen", "setup "} {
				if strings.Contains(low, banned) {
					t.Errorf("help mentions %q, which is not a public command", banned)
				}
			}
			for _, banned := range []string{"serve", "version --json", "migrate", "setup", "update-rollback-check"} {
				if _, ok := rows[banned]; ok {
					t.Errorf("help lists the internal command %q", banned)
				}
			}
		})
	}
}

func TestHelpMarksAndDeadlinesAreShown(t *testing.T) {
	r := runCLI(t, DefaultRegistry, runOpts{}, "help")
	rows := listedCommands(t, r.out)
	for path, want := range map[string][]string{
		"kit export-build":    {"[local] [root]"},
		"rebuild-agent":       {"[local] [root]", "1 h"},
		"verify-release":      {"[local]", "1 min"},
		"drill":               {"30 min"},
		"services stop":       {"2 min"},
		"restart":             {"2 min"},
		"drill record-manual": {"[admin]"},
		"catalog":             {" - "},
	} {
		for _, w := range want {
			if !strings.Contains(rows[path], w) {
				t.Errorf("the row of %s lacks %q: %q", path, w, rows[path])
			}
		}
	}
	if !strings.Contains(r.out, "[local] works without the agent; [root] needs sudo; [admin] needs the admin PIN") {
		t.Errorf("the marks are not explained:\n%s", r.out)
	}
}

func TestHelpEverySummaryHasASpanishText(t *testing.T) {
	reg := registeredCommands()
	for path, c := range reg {
		es, ok := helpES[path]
		switch {
		case !ok || strings.TrimSpace(es) == "":
			t.Errorf("`aictl %s` has no Spanish summary in helpES", path)
		case es == c.Summary:
			t.Errorf("the Spanish summary of %s is the English one", path)
		}
	}
	for path := range helpES {
		if _, ok := reg[path]; !ok {
			t.Errorf("helpES has a text for %q, which is not a command", path)
		}
	}
}

func TestHelpOfEveryCommandInBothLanguages(t *testing.T) {
	reg := registeredCommands()
	for path, c := range reg {
		args := strings.Fields(path)
		en := runCLI(t, DefaultRegistry, runOpts{}, append([]string{"help"}, args...)...)
		if en.code != 0 || !strings.Contains(en.out, "aictl [global flags] "+path) || !strings.Contains(en.out, c.Summary) {
			t.Errorf("help %s (en): exit %d\n%s%s", path, en.code, en.out, en.errOut)
		}
		es := runCLI(t, DefaultRegistry, runOpts{}, append([]string{"--lang", "es", "help"}, args...)...)
		if es.code != 0 || !strings.Contains(es.out, "aictl [opciones globales] "+path) || !strings.Contains(es.out, helpES[path]) || !strings.Contains(es.out, "Uso:") {
			t.Errorf("help %s (es): exit %d\n%s%s", path, es.code, es.out, es.errOut)
		}
		if m := c.Marks.String(); m != "" {
			if !strings.Contains(en.out, "Marks: "+m) || !strings.Contains(es.out, "Marcas: "+m) {
				t.Errorf("help %s does not show its marks %q", path, m)
			}
		}
		if len(c.Sub) > 0 {
			for _, s := range c.Sub {
				if !strings.Contains(en.out, s.Name) || !strings.Contains(es.out, s.Name) {
					t.Errorf("help %s does not list its subcommand %s", path, s.Name)
				}
			}
		}
	}
}

func TestGlobalFlagsAreDocumentedInSpanishToo(t *testing.T) {
	for _, f := range []string{"--json", "--lang", "--no-color", "-y, --yes", "-q, --quiet", "-v, --verbose", "--socket", "--url",
		"--token-file", "--timeout", "--pin-stdin", "-h, --help"} {
		if !strings.Contains(globalFlagsHelpES, f) {
			t.Errorf("the Spanish help lacks %s", f)
		}
	}
	if a, b := strings.Count(globalFlagsHelp, "\n"), strings.Count(globalFlagsHelpES, "\n"); a != b {
		t.Errorf("the global flags have %d lines in English and %d in Spanish", a, b)
	}
}

func TestTimeoutText(t *testing.T) {
	for d, want := range map[string]string{"0s": "-", "5s": "5 s", "10s": "10 s", "2m0s": "2 min", "30m0s": "30 min", "1h0m0s": "1 h", "3h0m0s": "3 h", "1h30m0s": "1 h 30 min", "90s": "1 min 30 s"} {
		dur, err := time.ParseDuration(d)
		if err != nil {
			t.Fatal(err)
		}
		if got := timeoutText(dur); got != want {
			t.Errorf("%s: %q, want %q", d, got, want)
		}
	}
}

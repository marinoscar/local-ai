package kit_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/kit"
)

func hex40(c string) string { return strings.Repeat(c, 40) }

func parse(t testing.TB, s string) kit.Pins {
	t.Helper()
	p, err := kit.ParsePins([]byte(s))
	if err != nil {
		t.Fatalf("ParsePins: %v\n%s", err, s)
	}
	return p
}

func encode(t testing.TB, p kit.Pins) string {
	t.Helper()
	b, err := p.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return string(b)
}

func TestPinsGoldenRoundTrip(t *testing.T) {
	want := readFile(t, filepath.Join("testdata", "pins", "valid.json"))
	p, err := kit.ParsePins([]byte(want))
	if err != nil {
		t.Fatal(err)
	}
	if got := encode(t, p); got != want {
		t.Fatalf("Encode(Parse(file)) differs from the file:\n%s\nwant\n%s", got, want)
	}
	if got := p.LlamaCommits(""); len(got) != 1 || got[0] != "0123456789abcdef0123456789abcdef01234567" {
		t.Errorf("LlamaCommits = %v", got)
	}
	if got := p.NodesOf(); strings.Join(got, ",") != "ai-n1,ai-n2,ai-n3" {
		t.Errorf("NodesOf = %v", got)
	}
}

func TestPinsEncodeIsCanonical(t *testing.T) {
	// A value built in any order encodes in the canonical order: lists sorted, nodes sorted.
	p := kit.Pins{
		UpdatedAt: epoch,
		LlamaCpp: []kit.CommitPin{
			{Commit: hex40("b"), Nodes: []string{"ai-n3", "ai-n1"}},
			{Commit: hex40("a"), Nodes: []string{"ai-n2"}},
		},
		Codenames: []kit.CodenamePin{
			{Codename: "resolute", Arch: "arm64", Nodes: []string{"ai-n4"}},
			{Codename: "noble", Arch: "amd64", Nodes: []string{"ai-n5"}},
			{Codename: "resolute", Arch: "amd64", Nodes: []string{"ai-n1"}},
		},
	}
	out := encode(t, p)
	if encode(t, p) != out {
		t.Fatal("Encode is not deterministic")
	}
	ia, ib := strings.Index(out, hex40("a")), strings.Index(out, hex40("b"))
	if ia < 0 || ib < 0 || ia > ib {
		t.Errorf("commits are not sorted:\n%s", out)
	}
	if !strings.Contains(out, "\"ai-n1\",\n        \"ai-n3\"") {
		t.Errorf("nodes are not sorted:\n%s", out)
	}
	in, ir := strings.Index(out, "noble"), strings.Index(out, "\"resolute\"")
	if in < 0 || in > ir {
		t.Errorf("codenames are not sorted:\n%s", out)
	}
	// every list is present, even when empty, so a reader never has to guess
	for _, key := range []string{`"whisper_cpp": []`, `"agent": []`, `"nvidia_branches": []`} {
		if !strings.Contains(out, key) {
			t.Errorf("missing %s in\n%s", key, out)
		}
	}
	// the encoder does not modify the caller's value
	if p.LlamaCpp[0].Commit != hex40("b") || p.LlamaCpp[0].Nodes[0] != "ai-n3" {
		t.Error("Encode reordered the caller's slices")
	}
}

func TestParsePinsRejects(t *testing.T) {
	good := `"llama_cpp": [], "whisper_cpp": [], "agent": [], "codenames": [], "nvidia_branches": []`
	entry := func(field, body string) string {
		return `{"schema_version": 1, "updated_at": "2026-11-02T00:00:00Z", ` + strings.Replace(good, `"`+field+`": []`, `"`+field+`": [`+body+`]`, 1) + `}`
	}
	c1 := hex40("1")
	cases := []struct {
		name    string
		in      string
		schema  bool // want ErrPinsSchema instead of ErrPinsCorrupt
		mention string
	}{
		{"empty", ``, false, ""},
		{"not json", `pins`, false, ""},
		{"an array", `[]`, false, ""},
		{"truncated", `{"schema_version": 1, "llama_cpp": [`, false, ""},
		{"no schema_version", `{"updated_at": "2026-11-02T00:00:00Z"}`, false, "schema_version is missing"},
		{"schema_version as a string", `{"schema_version": "1"}`, false, "schema_version"},
		{"schema_version 2", `{"schema_version": 2}`, true, "schema_version is 2"},
		{"schema_version 0", `{"schema_version": 0}`, true, "schema_version is 0"},
		{"newer schema is judged before the rest", `{"schema_version": 3, "llama_cpp": "garbage"}`, true, ""},
		{"updated_at not a time", `{"schema_version": 1, "updated_at": "yesterday"}`, false, "updated_at"},
		{"list of the wrong type", `{"schema_version": 1, "llama_cpp": {}}`, false, "llama_cpp"},
		{"short commit", entry("llama_cpp", `{"commit": "abc123", "nodes": ["ai-n1"]}`), false, "40 lowercase hex"},
		{"uppercase commit", entry("llama_cpp", `{"commit": "`+strings.ToUpper(hex40("a"))+`", "nodes": ["ai-n1"]}`), false, "40 lowercase hex"},
		{"non-hex commit", entry("whisper_cpp", `{"commit": "`+strings.Repeat("g", 40)+`", "nodes": ["ai-n1"]}`), false, "40 lowercase hex"},
		{"duplicate commit", entry("llama_cpp", `{"commit": "`+c1+`", "nodes": ["ai-n1"]}, {"commit": "`+c1+`", "nodes": ["ai-n2"]}`), false, "appears twice"},
		{"entry without nodes", entry("llama_cpp", `{"commit": "`+c1+`", "nodes": []}`), false, "lists no node"},
		{"entry with null nodes", entry("llama_cpp", `{"commit": "`+c1+`"}`), false, "lists no node"},
		{"node id with a slash", entry("llama_cpp", `{"commit": "`+c1+`", "nodes": ["a/b"]}`), false, "not a node id"},
		{"node id with .. ", entry("llama_cpp", `{"commit": "`+c1+`", "nodes": [".."]}`), false, "not a node id"},
		{"node listed twice", entry("llama_cpp", `{"commit": "`+c1+`", "nodes": ["ai-n1", "ai-n1"]}`), false, "twice"},
		{"agent tag with a space", entry("agent", `{"tag": "node v0.4.0", "nodes": ["ai-n1"]}`), false, "release tag"},
		{"agent tag empty", entry("agent", `{"tag": "", "nodes": ["ai-n1"]}`), false, "release tag"},
		{"duplicate agent tag", entry("agent", `{"tag": "node-v1", "nodes": ["a1"]}, {"tag": "node-v1", "nodes": ["a2"]}`), false, "appears twice"},
		{"codename uppercase", entry("codenames", `{"codename": "Resolute", "arch": "amd64", "nodes": ["ai-n1"]}`), false, "codename"},
		{"codename with a slash", entry("codenames", `{"codename": "re/solute", "arch": "amd64", "nodes": ["ai-n1"]}`), false, "codename"},
		{"arch unknown", entry("codenames", `{"codename": "resolute", "arch": "x86_64", "nodes": ["ai-n1"]}`), false, "amd64 or arm64"},
		{"duplicate codename and arch", entry("codenames", `{"codename": "resolute", "arch": "amd64", "nodes": ["a1"]}, {"codename": "resolute", "arch": "amd64", "nodes": ["a2"]}`), false, "appears twice"},
		{"same codename on two arches is fine only when distinct", entry("nvidia_branches", `{"branch": "595-open", "codename": "resolute", "arch": "amd64", "nodes": ["a1"]}, {"branch": "595-open", "codename": "resolute", "arch": "amd64", "nodes": ["a2"]}`), false, "appears twice"},
		{"branch empty", entry("nvidia_branches", `{"branch": "", "codename": "resolute", "arch": "amd64", "nodes": ["a1"]}`), false, "driver branch"},
		{"branch with a slash", entry("nvidia_branches", `{"branch": "595/open", "codename": "resolute", "arch": "amd64", "nodes": ["a1"]}`), false, "driver branch"},
		{"oversize", `{"schema_version": 1, "pad": "` + strings.Repeat("x", 4<<20) + `"}`, false, "larger than"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := kit.ParsePins([]byte(tc.in))
			if err == nil {
				t.Fatal("accepted")
			}
			want := kit.ErrPinsCorrupt
			if tc.schema {
				want = kit.ErrPinsSchema
			}
			if !errors.Is(err, want) {
				t.Errorf("err = %v, want %v", err, want)
			}
			if !kit.IsPinsDamaged(err) {
				t.Errorf("IsPinsDamaged(%v) = false", err)
			}
			if tc.mention != "" && !strings.Contains(err.Error(), tc.mention) {
				t.Errorf("err = %q, want it to mention %q", err, tc.mention)
			}
		})
	}
}

func TestParsePinsAccepts(t *testing.T) {
	cases := []struct{ name, in string }{
		{"minimal", `{"schema_version": 1}`},
		{"lists null", `{"schema_version": 1, "llama_cpp": null, "agent": null}`},
		{"unknown key inside an entry is ignored", `{"schema_version": 1, "agent": [{"tag": "node-v1", "nodes": ["a1"], "note": "x"}]}`},
		{"reported_at with an offset", `{"schema_version": 1, "agent": [{"tag": "node-v1", "nodes": ["a1"], "reported_at": "2026-11-02T01:00:00+01:00"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := parse(t, tc.in)
			if _, err := p.Encode(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPinsKeepUnknownTopLevelKeys(t *testing.T) {
	in := `{"schema_version": 1, "updated_at": "2026-11-02T00:00:00Z", "cloud": {"catalog": "2026.11.02-1"}, "agent": [{"tag": "node-v1", "nodes": ["a1"]}]}`
	p := parse(t, in)
	out := encode(t, p)
	if !strings.Contains(out, `"cloud"`) || !strings.Contains(out, `"2026.11.02-1"`) {
		t.Fatalf("an unknown top-level key was lost:\n%s", out)
	}
	// and through a merge
	own, _ := kit.Report(kit.NodePins{NodeID: "ai-n1", AgentTag: "node-v1"}, epoch)
	merged := kit.Merge(p, own, epoch)
	if !strings.Contains(encode(t, merged), `"cloud"`) {
		t.Errorf("a merge dropped the unknown key")
	}
	// the key is written after the known ones, so the known layout never moves
	if i, j := strings.Index(out, `"nvidia_branches"`), strings.Index(out, `"cloud"`); i < 0 || j < i {
		t.Errorf("unknown key is not after the known ones:\n%s", out)
	}
}

func TestReport(t *testing.T) {
	full := kit.NodePins{NodeID: "ai-n2", LlamaCommit: hex40("a"), WhisperCommit: hex40("b"), AgentTag: "node-v0.4.0",
		Codename: "resolute", Arch: "amd64", NvidiaBranch: "595-open"}
	cases := []struct {
		name string
		in   kit.NodePins
		want string // substrings the encoding must contain; "" for none
		err  string
	}{
		{"every field", full, `"595-open"`, ""},
		{"cpu node reports no nvidia and no whisper", kit.NodePins{NodeID: "ai-n7", LlamaCommit: hex40("a"), Codename: "resolute", Arch: "amd64"}, `"ai-n7"`, ""},
		{"nvidia codename defaults to the node's", full, `"codename": "resolute"`, ""},
		{"only a node id", kit.NodePins{NodeID: "ai-n1"}, `"schema_version": 1`, ""},
		{"bad node id", kit.NodePins{NodeID: "AI N1"}, "", "node id"},
		{"empty node id", kit.NodePins{}, "", "node id"},
		{"bad commit", kit.NodePins{NodeID: "ai-n1", LlamaCommit: "main"}, "", "40 lowercase hex"},
		{"codename without arch", kit.NodePins{NodeID: "ai-n1", Codename: "resolute"}, "", "amd64 or arm64"},
		{"arch without codename", kit.NodePins{NodeID: "ai-n1", Arch: "amd64"}, "", "codename"},
		{"bad tag", kit.NodePins{NodeID: "ai-n1", AgentTag: "../x"}, "", "release tag"},
		{"nvidia without arch", kit.NodePins{NodeID: "ai-n1", NvidiaBranch: "595-open", Codename: "resolute"}, "", "amd64 or arm64"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := kit.Report(tc.in, epoch.Add(321*time.Millisecond))
			if tc.err != "" {
				if !errors.Is(err, kit.ErrPinsCorrupt) || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want ErrPinsCorrupt mentioning %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			out := encode(t, p)
			if !strings.Contains(out, tc.want) {
				t.Errorf("encoding lacks %q:\n%s", tc.want, out)
			}
			if !p.UpdatedAt.Equal(epoch) {
				t.Errorf("UpdatedAt = %v, want the clock to the second %v", p.UpdatedAt, epoch)
			}
		})
	}
}

// ---------------------------------------------------------------------------------------------
// Merge

func day(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

// pinsOf builds a file value: an agent tag per (tag, nodes, reportedAgo) plus its file age.
func agentFile(updated time.Time, entries ...kit.AgentPin) kit.Pins {
	return kit.Pins{UpdatedAt: updated, Agent: entries}
}

func tags(p kit.Pins) string { return strings.Join(p.AgentTags(""), ",") }

func TestMerge(t *testing.T) {
	now := epoch
	ln := func(commit string, at time.Time, nodes ...string) kit.CommitPin {
		return kit.CommitPin{Commit: commit, Nodes: nodes, ReportedAt: at}
	}
	a1 := hex40("a")
	b1 := hex40("b")

	t.Run("add: a node's own entries are added to a root's", func(t *testing.T) {
		root := kit.Pins{UpdatedAt: now.Add(-day(3)), LlamaCpp: []kit.CommitPin{ln(a1, now.Add(-day(3)), "ai-n1")}}
		own, _ := kit.Report(kit.NodePins{NodeID: "ai-n3", LlamaCommit: b1, WhisperCommit: a1, AgentTag: "node-v0.4.0", Codename: "resolute", Arch: "amd64", NvidiaBranch: "595-open"}, now)
		got := kit.Merge(root, own, now)
		if c := got.LlamaCommits(""); len(c) != 2 || c[0] != a1 || c[1] != b1 {
			t.Errorf("llama commits = %v", c)
		}
		if c := got.WhisperCommits("ai-n3"); len(c) != 1 || c[0] != a1 {
			t.Errorf("whisper commits of ai-n3 = %v", c)
		}
		if tags(got) != "node-v0.4.0" || len(got.Codenames) != 1 || len(got.NvidiaBranches) != 1 {
			t.Errorf("merged = %s", got)
		}
		if !got.UpdatedAt.Equal(now) {
			t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, now)
		}
	})

	t.Run("keep others: nothing another node reported is removed", func(t *testing.T) {
		root := kit.Pins{UpdatedAt: now.Add(-day(1)), LlamaCpp: []kit.CommitPin{ln(a1, now.Add(-day(1)), "ai-n1", "ai-n2")}}
		// ai-n3 now builds b1 and does not mention a1 at all
		own, _ := kit.Report(kit.NodePins{NodeID: "ai-n3", LlamaCommit: b1}, now)
		got := kit.Merge(root, own, now)
		if c := got.LlamaCommits("ai-n1"); len(c) != 1 || c[0] != a1 {
			t.Errorf("ai-n1 lost its commit: %v", c)
		}
		if c := got.LlamaCommits("ai-n3"); len(c) != 1 || c[0] != b1 {
			t.Errorf("ai-n3 commits = %v", c)
		}
	})

	t.Run("a node that moves on keeps its old entry until it ages out", func(t *testing.T) {
		root := kit.Pins{UpdatedAt: now.Add(-day(10)), LlamaCpp: []kit.CommitPin{ln(a1, now.Add(-day(10)), "ai-n3")}}
		own, _ := kit.Report(kit.NodePins{NodeID: "ai-n3", LlamaCommit: b1}, now)
		got := kit.Merge(root, own, now)
		if c := got.LlamaCommits("ai-n3"); len(c) != 2 {
			t.Errorf("both commits must stay for ai-n3, got %v", c)
		}
	})

	t.Run("same entry: nodes are united and the later report wins", func(t *testing.T) {
		x := kit.Pins{UpdatedAt: now.Add(-day(5)), LlamaCpp: []kit.CommitPin{ln(a1, now.Add(-day(5)), "ai-n1")}}
		y := kit.Pins{UpdatedAt: now.Add(-day(2)), LlamaCpp: []kit.CommitPin{ln(a1, now.Add(-day(2)), "ai-n2", "ai-n1")}}
		got := kit.Merge(x, y, now)
		if len(got.LlamaCpp) != 1 {
			t.Fatalf("entries = %+v", got.LlamaCpp)
		}
		e := got.LlamaCpp[0]
		if strings.Join(e.Nodes, ",") != "ai-n1,ai-n2" || !e.ReportedAt.Equal(now.Add(-day(2))) {
			t.Errorf("entry = %+v", e)
		}
	})

	t.Run("stale: dropped after 180 days, kept at exactly 180", func(t *testing.T) {
		old := now.Add(-day(180) - time.Second)
		edge := now.Add(-day(180))
		root := kit.Pins{UpdatedAt: now.Add(-day(1)), LlamaCpp: []kit.CommitPin{ln(a1, old, "ai-n1"), ln(b1, edge, "ai-n2")}}
		got := kit.Merge(root, kit.Pins{}, now)
		if c := got.LlamaCommits(""); len(c) != 1 || c[0] != b1 {
			t.Errorf("commits after the merge = %v, want only the one reported exactly 180 days ago", c)
		}
	})

	t.Run("stale: every list ages out", func(t *testing.T) {
		old := now.Add(-day(181))
		root := kit.Pins{UpdatedAt: old,
			LlamaCpp:       []kit.CommitPin{ln(a1, old, "n1")},
			WhisperCpp:     []kit.CommitPin{ln(a1, old, "n1")},
			Agent:          []kit.AgentPin{{Tag: "node-v1", Nodes: []string{"n1"}, ReportedAt: old}},
			Codenames:      []kit.CodenamePin{{Codename: "resolute", Arch: "amd64", Nodes: []string{"n1"}, ReportedAt: old}},
			NvidiaBranches: []kit.NvidiaPin{{Branch: "595-open", Codename: "resolute", Arch: "amd64", Nodes: []string{"n1"}, ReportedAt: old}}}
		got := kit.Merge(root, kit.Pins{}, now)
		if len(got.NodesOf()) != 0 {
			t.Errorf("stale entries survived: %s", got)
		}
	})

	t.Run("a fresh report revives an entry that was about to expire", func(t *testing.T) {
		old := now.Add(-day(179))
		root := kit.Pins{UpdatedAt: old, LlamaCpp: []kit.CommitPin{ln(a1, old, "ai-n1")}}
		own, _ := kit.Report(kit.NodePins{NodeID: "ai-n1", LlamaCommit: a1}, now)
		later := now.Add(day(10)) // the entry would be stale by then without the report
		got := kit.Merge(root, own, later)
		if c := got.LlamaCommits(""); len(c) != 1 {
			t.Errorf("a reported entry expired: %s", got)
		}
	})

	t.Run("an entry without a report time ages from the updated_at of its file", func(t *testing.T) {
		fresh := agentFile(now.Add(-day(10)), kit.AgentPin{Tag: "node-v1", Nodes: []string{"a1"}})
		stale := agentFile(now.Add(-day(200)), kit.AgentPin{Tag: "node-v0", Nodes: []string{"a1"}})
		got := kit.Merge(fresh, stale, now)
		if tags(got) != "node-v1" {
			t.Errorf("tags = %s, want node-v1", tags(got))
		}
		if !got.Agent[0].ReportedAt.Equal(now.Add(-day(10))) {
			t.Errorf("the report time was not made explicit: %v", got.Agent[0].ReportedAt)
		}
	})

	t.Run("an undated entry in an undated file never expires", func(t *testing.T) {
		got := kit.Merge(kit.Pins{Agent: []kit.AgentPin{{Tag: "node-v1", Nodes: []string{"a1"}}}}, kit.Pins{}, now)
		if tags(got) != "node-v1" {
			t.Errorf("an undated pin was dropped: %s", got)
		}
	})

	t.Run("merging nothing is a pure prune", func(t *testing.T) {
		got := kit.Merge(kit.Pins{}, kit.Pins{}, now)
		if len(got.NodesOf()) != 0 || !got.UpdatedAt.IsZero() {
			t.Errorf("empty merge = %+v", got)
		}
		if _, err := got.Encode(); err != nil {
			t.Errorf("an empty merge result must encode: %v", err)
		}
	})

	t.Run("the result is valid and canonical whatever went in", func(t *testing.T) {
		own, _ := kit.Report(kit.NodePins{NodeID: "ai-n3", LlamaCommit: b1, Codename: "resolute", Arch: "amd64"}, now)
		got := kit.Merge(parse(t, readFile(t, filepath.Join("testdata", "pins", "valid.json"))), own, now)
		if err := got.Validate(); err != nil {
			t.Fatal(err)
		}
		if _, err := kit.ParsePins([]byte(encode(t, got))); err != nil {
			t.Fatal(err)
		}
	})
}

// Commutative, idempotent and associative for a fixed clock, over a set of values that exercises every
// list, the stale boundary, undated entries and unknown keys.
func TestMergeLaws(t *testing.T) {
	now := epoch
	mk := func(s string) kit.Pins { return parse(t, s) }
	ts := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	values := map[string]kit.Pins{
		"empty": {},
		"valid": mk(readFile(t, filepath.Join("testdata", "pins", "valid.json"))),
		"fresh": mk(`{"schema_version": 1, "updated_at": "` + ts(day(2)) + `",
			"llama_cpp": [{"commit": "` + hex40("a") + `", "nodes": ["ai-n1"], "reported_at": "` + ts(day(2)) + `"}],
			"agent": [{"tag": "node-v0.4.0", "nodes": ["ai-n1", "ai-n9"]}],
			"codenames": [{"codename": "resolute", "arch": "amd64", "nodes": ["ai-n1"]}],
			"nvidia_branches": [{"branch": "595-open", "codename": "resolute", "arch": "amd64", "nodes": ["ai-n1"]}]}`),
		"older": mk(`{"schema_version": 1, "updated_at": "` + ts(day(150)) + `",
			"llama_cpp": [{"commit": "` + hex40("a") + `", "nodes": ["ai-n2"], "reported_at": "` + ts(day(150)) + `"}, {"commit": "` + hex40("c") + `", "nodes": ["ai-n2"]}],
			"whisper_cpp": [{"commit": "` + hex40("d") + `", "nodes": ["ai-n2"]}],
			"agent": [{"tag": "node-v0.3.0", "nodes": ["ai-n2"]}]}`),
		"stale": mk(`{"schema_version": 1, "updated_at": "` + ts(day(400)) + `",
			"llama_cpp": [{"commit": "` + hex40("e") + `", "nodes": ["ai-n3"]}],
			"codenames": [{"codename": "noble", "arch": "arm64", "nodes": ["ai-n3"]}]}`),
		"edge": mk(`{"schema_version": 1, "updated_at": "` + ts(day(180)) + `",
			"whisper_cpp": [{"commit": "` + hex40("f") + `", "nodes": ["ai-n4"]}]}`),
		"extra-1": mk(`{"schema_version": 1, "updated_at": "` + ts(day(1)) + `", "note": "one"}`),
		"extra-2": mk(`{"schema_version": 1, "updated_at": "` + ts(day(1)) + `", "note": "two"}`),
	}
	var names []string
	for n := range values {
		names = append(names, n)
	}
	sortStrings(names)
	m := func(a, b kit.Pins) string { return encode(t, kit.Merge(a, b, now)) }
	for _, an := range names {
		a := values[an]
		if got, want := m(a, a), m(a, kit.Pins{}); got != want {
			t.Errorf("merge(%s, %s) differs from merge(%s, empty):\n%s\n%s", an, an, an, got, want)
		}
		for _, bn := range names {
			b := values[bn]
			ab := m(a, b)
			if ba := m(b, a); ab != ba {
				t.Errorf("not commutative for %s, %s:\n%s\n%s", an, bn, ab, ba)
			}
			merged := kit.Merge(a, b, now)
			if got := m(merged, b); got != ab {
				t.Errorf("not idempotent for %s, %s: merging b again changed the result", an, bn)
			}
			if got := m(merged, a); got != ab {
				t.Errorf("not idempotent for %s, %s: merging a again changed the result", an, bn)
			}
			for _, cn := range names {
				c := values[cn]
				left := encode(t, kit.Merge(kit.Merge(a, b, now), c, now))
				right := encode(t, kit.Merge(a, kit.Merge(b, c, now), now))
				if left != right {
					t.Fatalf("not associative for %s, %s, %s:\n%s\n%s", an, bn, cn, left, right)
				}
			}
		}
	}
}

func TestPinsQueries(t *testing.T) {
	p := parse(t, `{"schema_version": 1,
		"llama_cpp": [{"commit": "`+hex40("b")+`", "nodes": ["ai-n1", "ai-n2"]}, {"commit": "`+hex40("a")+`", "nodes": ["ai-n3"]}],
		"whisper_cpp": [{"commit": "`+hex40("c")+`", "nodes": ["ai-n3"]}],
		"agent": [{"tag": "node-v0.4.0", "nodes": ["ai-n3"]}, {"tag": "node-v0.3.0", "nodes": ["ai-n1"]}]}`)
	cases := []struct {
		name string
		got  []string
		want string
	}{
		{"llama all", p.LlamaCommits(""), hex40("a") + "," + hex40("b")},
		{"llama of ai-n1", p.LlamaCommits("ai-n1"), hex40("b")},
		{"llama of an unknown node", p.LlamaCommits("ai-n9"), ""},
		{"whisper all", p.WhisperCommits(""), hex40("c")},
		{"whisper of ai-n1", p.WhisperCommits("ai-n1"), ""},
		{"agent all", p.AgentTags(""), "node-v0.3.0,node-v0.4.0"},
		{"agent of ai-n3", p.AgentTags("ai-n3"), "node-v0.4.0"},
		{"nodes", p.NodesOf(), "ai-n1,ai-n2,ai-n3"},
	}
	for _, tc := range cases {
		if got := strings.Join(tc.got, ","); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------------------------
// Files

func TestUpdatePinsFiles(t *testing.T) {
	now := epoch
	own := func(node, commit string) kit.Pins {
		p, err := kit.Report(kit.NodePins{NodeID: node, LlamaCommit: commit, Codename: "resolute", Arch: "amd64"}, now)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	newPath := func(t *testing.T) string {
		return filepath.Join(t.TempDir(), "kit", "catalog", kit.PinsFileName)
	}
	seed := func(t *testing.T, path, content string) {
		t.Helper()
		writeFile(t, path, content)
	}

	t.Run("a missing file is created", func(t *testing.T) {
		path := newPath(t)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := kit.UpdatePins(config.OSFS{}, path, own("ai-n1", hex40("a")), now)
		if err != nil {
			t.Fatal(err)
		}
		onDisk, err := kit.ReadPins(config.OSFS{}, path)
		if err != nil || encode(t, onDisk) != encode(t, got) {
			t.Fatalf("file = %v, %v", onDisk, err)
		}
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0o644 {
			t.Errorf("mode = %v, want 0644", info.Mode().Perm())
		}
	})

	t.Run("two nodes merge on one drive and keep each other's entries", func(t *testing.T) {
		path := newPath(t)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := kit.UpdatePins(config.OSFS{}, path, own("ai-n1", hex40("a")), now); err != nil {
			t.Fatal(err)
		}
		got, err := kit.UpdatePins(config.OSFS{}, path, own("ai-n2", hex40("b")), now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if c := got.LlamaCommits(""); len(c) != 2 {
			t.Errorf("commits = %v", c)
		}
		if n := got.NodesOf(); strings.Join(n, ",") != "ai-n1,ai-n2" {
			t.Errorf("nodes = %v", n)
		}
		// repeating the same update changes nothing but the time
		again, err := kit.UpdatePins(config.OSFS{}, path, own("ai-n2", hex40("b")), now.Add(time.Hour))
		if err != nil || encode(t, again) != encode(t, got) {
			t.Errorf("a repeated update changed the file: %v", err)
		}
	})

	t.Run("a damaged file is reported and left exactly as it is", func(t *testing.T) {
		for name, content := range map[string]string{
			"not json":        "{not json",
			"newer schema":    `{"schema_version": 2, "future": true}`,
			"bad entry":       `{"schema_version": 1, "llama_cpp": [{"commit": "x", "nodes": ["a"]}]}`,
			"empty file":      "",
			"trailing binary": "\x00\x01\x02",
		} {
			t.Run(name, func(t *testing.T) {
				path := newPath(t)
				seed(t, path, content)
				_, err := kit.UpdatePins(config.OSFS{}, path, own("ai-n1", hex40("a")), now)
				if !kit.IsPinsDamaged(err) {
					t.Fatalf("err = %v, want a damaged-pins error", err)
				}
				if got := readFile(t, path); got != content {
					t.Errorf("the damaged file was changed: %q", got)
				}
				entries, _ := os.ReadDir(filepath.Dir(path))
				if len(entries) != 1 {
					t.Errorf("stray files left: %v", entries)
				}
			})
		}
	})

	t.Run("an I/O error reading the file is not mistaken for a missing one", func(t *testing.T) {
		path := newPath(t)
		seed(t, path, readFile(t, filepath.Join("testdata", "pins", "valid.json")))
		ffs := fake.New(config.OSFS{}).FailOn("readfile", 1)
		_, err := kit.UpdatePins(ffs, path, own("ai-n1", hex40("a")), now)
		if !errors.Is(err, fake.ErrInjected) || kit.IsPinsDamaged(err) || errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("err = %v", err)
		}
		if readFile(t, path) != readFile(t, filepath.Join("testdata", "pins", "valid.json")) {
			t.Error("the file changed")
		}
	})

	t.Run("a write that fails at every step leaves the old file and no temp file", func(t *testing.T) {
		for _, op := range []string{"open", "write", "chmod", "sync", "close", "rename"} {
			t.Run(op, func(t *testing.T) {
				path := newPath(t)
				orig := readFile(t, filepath.Join("testdata", "pins", "valid.json"))
				seed(t, path, orig)
				ffs := fake.New(config.OSFS{}).FailOn(op, 1)
				_, err := kit.UpdatePins(ffs, path, own("ai-n9", hex40("9")), now)
				if err == nil {
					t.Fatal("the failure was swallowed")
				}
				if got := readFile(t, path); got != orig {
					t.Errorf("the file changed after a failed %s", op)
				}
				entries, _ := os.ReadDir(filepath.Dir(path))
				if len(entries) != 1 {
					t.Errorf("a temp file was left behind: %v", entries)
				}
			})
		}
	})

	t.Run("WritePins refuses an invalid value and writes nothing", func(t *testing.T) {
		path := newPath(t)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		err := kit.WritePins(config.OSFS{}, path, kit.Pins{LlamaCpp: []kit.CommitPin{{Commit: "short", Nodes: []string{"a"}}}})
		if !errors.Is(err, kit.ErrPinsCorrupt) {
			t.Fatalf("err = %v", err)
		}
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("a file was created: %v", err)
		}
	})

	t.Run("ReadPins of a missing file satisfies fs.ErrNotExist", func(t *testing.T) {
		_, err := kit.ReadPins(config.OSFS{}, filepath.Join(t.TempDir(), "none.json"))
		if !errors.Is(err, fs.ErrNotExist) || kit.IsPinsDamaged(err) {
			t.Fatalf("err = %v", err)
		}
	})
}

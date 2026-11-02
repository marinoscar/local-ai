package cli

import (
	"strings"
	"testing"
	"time"
)

func TestRegistryValidation(t *testing.T) {
	ok := func(*Call) error { return nil }
	cases := []struct {
		name string
		cmd  *Command
		want string // substring of the error, "" for valid
	}{
		{"valid", &Command{Name: "status", Summary: "s", Run: ok}, ""},
		{"valid group", &Command{Name: "key", Summary: "s", Sub: []*Command{{Name: "show", Summary: "s", Run: ok}}}, ""},
		{"no name", &Command{Summary: "s", Run: ok}, "invalid command name"},
		{"name with a space", &Command{Name: "a b", Summary: "s", Run: ok}, "invalid command name"},
		{"name with a dash prefix", &Command{Name: "-x", Summary: "s", Run: ok}, "invalid command name"},
		{"no summary", &Command{Name: "x", Run: ok}, "no summary"},
		{"no Run and no subcommands", &Command{Name: "x", Summary: "s"}, "neither Run nor subcommands"},
		{"help is reserved", &Command{Name: "help", Summary: "s", Run: ok}, "reserved"},
		{"invalid subcommand", &Command{Name: "x", Summary: "s", Sub: []*Command{{Name: "", Summary: "s", Run: ok}}}, "invalid command name"},
		{"duplicate subcommand", &Command{Name: "x", Summary: "s", Sub: []*Command{
			{Name: "a", Summary: "s", Run: ok}, {Name: "a", Summary: "s", Run: ok}}}, "declared twice"},
	}
	for _, tc := range cases {
		err := NewRegistry().Add(tc.cmd)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestRegistryDuplicateAndPanic(t *testing.T) {
	r := NewRegistry()
	c := &Command{Name: "x", Summary: "s", Run: func(*Call) error { return nil }}
	if err := r.Add(c); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(c); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Errorf("duplicate: %v", err)
	}
	defer func() {
		if recover() == nil {
			t.Error("MustAdd of a duplicate did not panic")
		}
	}()
	r.MustAdd(c)
}

func TestRegistryListsSortedAndMarks(t *testing.T) {
	r := NewRegistry()
	for _, n := range []string{"zeta", "alpha", "mid"} {
		r.MustAdd(&Command{Name: n, Summary: n, Run: func(*Call) error { return nil }})
	}
	var names []string
	for _, c := range r.Commands() {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "alpha,mid,zeta" {
		t.Errorf("order = %v", names)
	}
	cases := map[Marks]string{
		0:                                "",
		MarkLocal:                        "[local]",
		MarkLocal | MarkRoot:             "[local] [root]",
		MarkAdmin:                        "[admin]",
		MarkLocal | MarkRoot | MarkAdmin: "[local] [root] [admin]",
	}
	for m, want := range cases {
		if got := m.String(); got != want {
			t.Errorf("marks %d = %q, want %q", m, got, want)
		}
	}
}

// Register adds to DefaultRegistry, the one Main uses; the seam later commands plug into.
func TestRegisterSeam(t *testing.T) {
	name := "seam-test-command"
	Register(&Command{Name: name, Summary: "x", Timeout: 5 * time.Second, Run: func(*Call) error { return nil }})
	defer delete(DefaultRegistry.cmds, name)
	if _, ok := DefaultRegistry.Lookup(name); !ok {
		t.Fatal("Register did not reach DefaultRegistry")
	}
}

// A command that has its own Run and subcommands takes an unknown word as a positional argument.
func TestResolveRunAndSub(t *testing.T) {
	r := NewRegistry()
	r.MustAdd(&Command{Name: "catalog", Summary: "s",
		Run: func(*Call) error { return nil },
		Sub: []*Command{{Name: "lint", Summary: "s", Run: func(*Call) error { return nil }}}})
	res, unknown, err := r.resolve([]string{"catalog", "lint", "dir"})
	if err != nil || unknown != "" || strings.Join(res.path, " ") != "catalog lint" || strings.Join(res.rest, " ") != "dir" {
		t.Errorf("lint: %+v %q %v", res, unknown, err)
	}
	res, unknown, err = r.resolve([]string{"catalog", "other"})
	if err != nil || unknown != "" || strings.Join(res.path, " ") != "catalog" || strings.Join(res.rest, " ") != "other" {
		t.Errorf("positional: %+v %q %v", res, unknown, err)
	}
}

package cli

import (
	"strings"
	"testing"
)

func lateCmd(name string) *Command {
	return &Command{Name: name, Summary: name + ".", Run: func(*Call) error { return nil }}
}

func subNames(c *Command) string {
	var n []string
	for _, s := range c.Sub {
		n = append(n, s.Name)
	}
	return strings.Join(n, ",")
}

// A hook adds to a group another file registered, whatever the file order, and creates the group
// when no other file has.
func TestAttachJoinsOrCreatesTheGroup(t *testing.T) {
	r := NewRegistry()
	r.MustAdd(&Command{Name: "ark", Summary: "The ark.", Sub: []*Command{lateCmd("status")}})
	attach(r, []string{"ark"}, lateCmd("sync"))
	attach(r, []string{"cloud"}, lateCmd("enroll"))
	ark, _ := r.Lookup("ark")
	cloud, ok := r.Lookup("cloud")
	if subNames(ark) != "status,sync" || !ok || subNames(cloud) != "enroll" || cloud.Summary == "" {
		t.Fatalf("ark: %s, cloud: %v", subNames(ark), cloud)
	}
	// What the group is created with is a pure group, so the tree still resolves.
	if res, _, err := r.resolve([]string{"cloud", "enroll"}); err != nil || strings.Join(res.path, " ") != "cloud enroll" {
		t.Errorf("resolve: %v %v", res.path, err)
	}
}

// A nested path (ark drives set-profile) keeps the drives command another file registered, with its
// own Run, and a missing level is created.
func TestAttachNested(t *testing.T) {
	r := NewRegistry()
	drives := lateCmd("drives")
	r.MustAdd(&Command{Name: "ark", Summary: "The ark.", Sub: []*Command{drives}})
	attach(r, []string{"ark", "drives"}, lateCmd("set-profile"))
	if subNames(drives) != "set-profile" || drives.Run == nil {
		t.Fatalf("drives: %s run=%v", subNames(drives), drives.Run != nil)
	}
	attach(r, []string{"profile", "x"}, lateCmd("y"))
	prof, _ := r.Lookup("profile")
	if subNames(prof) != "x" || subNames(prof.Sub[0]) != "y" {
		t.Fatalf("profile: %s", subNames(prof))
	}
}

// A command takes over from a stub of the same name.
func TestAttachReplacesAStub(t *testing.T) {
	r := NewRegistry()
	stub := lateCmd("refresh")
	r.MustAdd(&Command{Name: "catalog", Summary: "c.", Sub: []*Command{lateCmd("lint"), stub}})
	real := lateCmd("refresh")
	attach(r, []string{"catalog"}, real)
	cat, _ := r.Lookup("catalog")
	if subNames(cat) != "lint,refresh" || cat.Sub[1] != real {
		t.Fatalf("catalog: %s", subNames(cat))
	}
}

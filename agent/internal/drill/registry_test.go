package drill

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func run0(context.Context, *Env) Outcome { return Outcome{Result: ResultPass} }

func ids(cs []Check) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

func TestRegistryAddValidation(t *testing.T) {
	good := Check{ID: "drl.fence", Area: "net", Order: 1, Run: run0}
	for name, mod := range map[string]func(*Check){
		"bad id prefix":    func(c *Check) { c.ID = "fence" },
		"empty name":       func(c *Check) { c.ID = "drl." },
		"id with a slash":  func(c *Check) { c.ID = "drl.a/b" },
		"id with a space":  func(c *Check) { c.ID = "drl.a b" },
		"id too long":      func(c *Check) { c.ID = "drl." + strings.Repeat("a", 250) },
		"no area":          func(c *Check) { c.Area = " " },
		"no run":           func(c *Check) { c.Run = nil },
		"no order":         func(c *Check) { c.Order = 0 },
		"negative timeout": func(c *Check) { c.Timeout = -time.Second },
		"unknown phase":    func(c *Check) { c.Phase = 9 },
	} {
		t.Run(name, func(t *testing.T) {
			c := good
			mod(&c)
			if err := NewRegistry().Add(c); err == nil {
				t.Fatalf("Add accepted %+v", c)
			}
		})
	}
	reg := NewRegistry()
	for _, id := range []string{"drl.fence", "drl.llm.answer.qwen3.5-9b-q4km", "drl.kit.rebuild_dry_run"} {
		c := good
		c.ID = id
		if err := reg.Add(c); err != nil {
			t.Errorf("Add(%s): %v", id, err)
		}
	}
	if err := reg.Add(good); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("a duplicate id must be refused, got %v", err)
	}
}

func TestRegistrySelectOrdersByNumberThenRegistration(t *testing.T) {
	reg := NewRegistry()
	for _, c := range []Check{
		{ID: "drl.b", Order: 10}, {ID: "drl.a", Order: 2}, {ID: "drl.c", Order: 10}, {ID: "drl.d", Order: 1},
	} {
		c.Area, c.Run = "x", run0
		if err := reg.Add(c); err != nil {
			t.Fatal(err)
		}
	}
	got, err := reg.Select(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"drl.d", "drl.a", "drl.b", "drl.c"}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("order = %v, want %v", ids(got), want)
	}
}

func TestRegistrySelectOnly(t *testing.T) {
	reg := NewRegistry()
	for i, id := range []string{"drl.fence", "drl.stt.en", "drl.stt.es", "drl.stt", "drl.sttx", "drl.llm.answer.m1", "drl.llm.answer.m2"} {
		if id == "drl.stt" {
			continue // a leaf and a group of the same name is not a case worth supporting
		}
		if err := reg.Add(Check{ID: id, Area: "x", Order: i + 1, Run: run0}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		only []string
		want []string
	}{
		{"exact", []string{"drl.fence"}, []string{"drl.fence"}},
		{"group prefix", []string{"drl.stt"}, []string{"drl.stt.en", "drl.stt.es"}},
		{"a prefix is a dot boundary, not a substring", []string{"drl.st"}, nil},
		{"two groups keep registration order", []string{"drl.llm.answer", "drl.fence"}, []string{"drl.fence", "drl.llm.answer.m1", "drl.llm.answer.m2"}},
		{"duplicates select once", []string{"drl.fence", "drl.fence"}, []string{"drl.fence"}},
		{"unknown", []string{"drl.nope"}, nil},
		{"one unknown among known", []string{"drl.fence", "drl.nope", "drl.nada"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := reg.Select(context.Background(), tc.only)
			if tc.want == nil {
				var unknown *UnknownCheckError
				if !errors.As(err, &unknown) {
					t.Fatalf("Select(%v) = %v, %v, want *UnknownCheckError", tc.only, ids(got), err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(ids(got), tc.want) {
				t.Fatalf("Select(%v) = %v, %v, want %v", tc.only, ids(got), err, tc.want)
			}
		})
	}
	_, err := reg.Select(context.Background(), []string{"drl.fence", "drl.nope", "drl.nada"})
	var unknown *UnknownCheckError
	if !errors.As(err, &unknown) || !reflect.DeepEqual(unknown.IDs, []string{"drl.nope", "drl.nada"}) {
		t.Fatalf("the error must name every unknown id: %v", err)
	}
}

func TestRegistryGroups(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Add(Check{ID: "drl.fence", Area: "net", Order: 1, Run: run0}); err != nil {
		t.Fatal(err)
	}
	models := []string{"m1", "m2"}
	if err := reg.AddGroup("llm", func(context.Context) ([]Check, error) {
		var out []Check
		for _, m := range models {
			out = append(out, Check{ID: "drl.llm.answer." + m, Area: "llm", Order: 5, Run: run0})
		}
		return out, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.AddGroup("llm", func(context.Context) ([]Check, error) { return nil, nil }); err == nil {
		t.Fatal("a group registered twice must be refused")
	}
	if err := reg.AddGroup("", nil); err == nil {
		t.Fatal("an anonymous group must be refused")
	}
	got, err := reg.Select(context.Background(), nil)
	if err != nil || !reflect.DeepEqual(ids(got), []string{"drl.fence", "drl.llm.answer.m1", "drl.llm.answer.m2"}) {
		t.Fatalf("Select = %v, %v", ids(got), err)
	}
	// The expander runs for every selection: a profile change shows in the next run.
	models = []string{"m3"}
	got, _ = reg.Select(context.Background(), []string{"drl.llm"})
	if !reflect.DeepEqual(ids(got), []string{"drl.llm.answer.m3"}) {
		t.Fatalf("expander result is not fresh: %v", ids(got))
	}
}

func TestRegistryGroupErrors(t *testing.T) {
	for name, x := range map[string]Expander{
		"expander error": func(context.Context) ([]Check, error) { return nil, errors.New("profile unreadable") },
		"invalid check":  func(context.Context) ([]Check, error) { return []Check{{ID: "bad"}}, nil },
		"duplicate of a static check": func(context.Context) ([]Check, error) {
			return []Check{{ID: "drl.fence", Area: "x", Order: 1, Run: run0}}, nil
		},
		"duplicate inside the group": func(context.Context) ([]Check, error) {
			c := Check{ID: "drl.g.a", Area: "x", Order: 1, Run: run0}
			return []Check{c, c}, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			reg := NewRegistry()
			_ = reg.Add(Check{ID: "drl.fence", Area: "net", Order: 1, Run: run0})
			_ = reg.AddGroup("g", x)
			if _, err := reg.Select(context.Background(), nil); err == nil {
				t.Fatal("Select must fail")
			}
		})
	}
}

func TestNormalizeOnly(t *testing.T) {
	got, err := NormalizeOnly([]string{" drl.a ", "drl.b", "drl.a"})
	if err != nil || !reflect.DeepEqual(got, []string{"drl.a", "drl.b"}) {
		t.Fatalf("NormalizeOnly = %v, %v", got, err)
	}
	if got, err := NormalizeOnly(nil); err != nil || len(got) != 0 {
		t.Fatalf("NormalizeOnly(nil) = %v, %v", got, err)
	}
	long := make([]string, maxOnlyEntries+1)
	for i := range long {
		long[i] = "drl.x"
	}
	for name, in := range map[string][]string{
		"empty entry": {"drl.a", " "},
		"too long":    {strings.Repeat("a", maxIDLen+1)},
		"too many":    long,
	} {
		if _, err := NormalizeOnly(in); err == nil {
			t.Errorf("%s: NormalizeOnly accepted %d entries", name, len(in))
		}
	}
}

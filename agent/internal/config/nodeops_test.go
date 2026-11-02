package config_test

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// edit applies fn to the top-level mapping of in and returns the re-encoded YAML.
func edit(t *testing.T, in string, fn func(root *yaml.Node) error) (string, error) {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(in), &doc); err != nil {
		t.Fatal(err)
	}
	if err := fn(doc.Content[0]); err != nil {
		return "", err
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(out), nil
}

func TestSetKeyReplacesInPlaceAndKeepsComments(t *testing.T) {
	in := "a: 1 # one\nb:\n  c: x # cee\n  d: y\ne: 5\n"
	out, err := edit(t, in, func(r *yaml.Node) error { return config.SetKey(r, "z", "b", "c") })
	if err != nil {
		t.Fatal(err)
	}
	want := "a: 1 # one\nb:\n    c: z # cee\n    d: y\ne: 5\n"
	if out != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
}

func TestSetKeyCreatesMissingMappingsAndAppends(t *testing.T) {
	out, err := edit(t, "a: 1\n", func(r *yaml.Node) error { return config.SetKey(r, 7, "b", "c", "d") })
	if err != nil {
		t.Fatal(err)
	}
	if out != "a: 1\nb:\n    c:\n        d: 7\n" {
		t.Errorf("got\n%s", out)
	}
}

func TestSetKeyTurnsANullValueIntoAGroup(t *testing.T) {
	out, err := edit(t, "ui:\nother: 1\n", func(r *yaml.Node) error { return config.SetKey(r, "es", "ui", "language") })
	if err != nil {
		t.Fatal(err)
	}
	if out != "ui:\n    language: es\nother: 1\n" {
		t.Errorf("got\n%s", out)
	}
}

func TestSetKeyKeepsQuotingOfTextAndFlowStyleOfLists(t *testing.T) {
	in := "a: \"old\"\nl: [x, y]\nn: \"5\"\n"
	out, err := edit(t, in, func(r *yaml.Node) error {
		if err := config.SetKey(r, "new", "a"); err != nil {
			return err
		}
		if err := config.SetKey(r, []string{"p", "q"}, "l"); err != nil {
			return err
		}
		return config.SetKey(r, 5, "n") // a number replacing quoted text must not stay quoted
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "a: \"new\"\nl: [p, q]\nn: 5\n" {
		t.Errorf("got\n%s", out)
	}
}

func TestSetKeyRefusesToDescendThroughAScalar(t *testing.T) {
	_, err := edit(t, "a: 1\n", func(r *yaml.Node) error { return config.SetKey(r, "x", "a", "b") })
	if err == nil || !strings.Contains(err.Error(), "not a group") {
		t.Errorf("err = %v", err)
	}
	if _, err := edit(t, "a: 1\n", func(r *yaml.Node) error { return config.SetKey(r, "x") }); err == nil {
		t.Error("an empty path must be refused")
	}
}

func TestSetKeyRefusesAliases(t *testing.T) {
	in := "base: &b\n  x: 1\ncopy: *b\n"
	if _, err := edit(t, in, func(r *yaml.Node) error { return config.SetKey(r, 2, "copy", "x") }); err == nil {
		t.Error("editing through an alias must be refused: it would change the anchor too")
	}
}

func TestDeleteAndLookupKey(t *testing.T) {
	in := "a: 1\nb:\n  c: 2\n  d: 3\n"
	out, err := edit(t, in, func(r *yaml.Node) error {
		if config.LookupKey(r, "b", "c") == nil || config.LookupKey(r, "b", "zz") != nil || config.LookupKey(r, "a", "x") != nil {
			t.Error("LookupKey wrong")
		}
		if !config.DeleteKey(r, "b", "c") || config.DeleteKey(r, "b", "c") || config.DeleteKey(r, "nope") || config.DeleteKey(r) {
			t.Error("DeleteKey wrong")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "a: 1\nb:\n    d: 3\n" {
		t.Errorf("got\n%s", out)
	}
}

func TestUpdateCanDeleteAnOptionalKey(t *testing.T) {
	s, path := openStore(t, example(t))
	err := s.Update(func(r *yaml.Node) error {
		if !config.DeleteKey(r, "node", "public_host") {
			t.Error("public_host exists")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readString(t, path), "public_host") || s.Get().Node.PublicHost != nil {
		t.Error("the key must be gone")
	}
}

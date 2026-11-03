package importer

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// memStore is an Updater over a yaml document held in memory, the way *config.Store hands it out.
type memStore struct {
	text string
	err  error
}

func (m *memStore) Update(fn func(root *yaml.Node) error) error {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(m.text), &doc); err != nil {
		return err
	}
	root := doc.Content[0]
	if err := fn(root); err != nil {
		return err
	}
	if m.err != nil {
		return m.err
	}
	out, err := yaml.Marshal(&doc)
	m.text = string(out)
	return err
}

func overrides(t *testing.T, text string) (addModels, removeModels, addContent []string) {
	t.Helper()
	var f struct {
		Overrides struct {
			AddModels     []string `yaml:"add_models"`
			RemoveModels  []string `yaml:"remove_models"`
			AddContent    []string `yaml:"add_content"`
			RemoveContent []string `yaml:"remove_content"`
		} `yaml:"profile_overrides"`
	}
	if err := yaml.Unmarshal([]byte(text), &f); err != nil {
		t.Fatal(err)
	}
	return f.Overrides.AddModels, f.Overrides.RemoveModels, f.Overrides.AddContent
}

func TestNodeYAMLAddsToTheOverridesAndKeepsWhatWasThere(t *testing.T) {
	store := &memStore{text: `# my node
profile: laptop-offline
profile_overrides:
  add_models: [m-b]
  remove_models: [m-x, m-y]
  budget_bytes: 100
logging:
  level: info
`}
	if err := (NodeYAML{Store: store}).AddItems([]string{"m-a", "m-b", "m-x"}, []string{"w-1"}); err != nil {
		t.Fatal(err)
	}
	addM, rmM, addC := overrides(t, store.text)
	if strings.Join(addM, ",") != "m-a,m-b,m-x" || strings.Join(rmM, ",") != "m-y" || strings.Join(addC, ",") != "w-1" {
		t.Fatalf("add_models %v remove_models %v add_content %v\n%s", addM, rmM, addC, store.text)
	}
	for _, keep := range []string{"# my node", "budget_bytes: 100", "level: info", "profile: laptop-offline"} {
		if !strings.Contains(store.text, keep) {
			t.Errorf("node.yaml lost %q:\n%s", keep, store.text)
		}
	}

	t.Run("no overrides block yet: it is created", func(t *testing.T) {
		s := &memStore{text: "profile: laptop-offline\n"}
		if err := (NodeYAML{Store: s}).AddItems(nil, []string{"w-1", "w-1"}); err != nil {
			t.Fatal(err)
		}
		_, _, addC := overrides(t, s.text)
		if strings.Join(addC, ",") != "w-1" {
			t.Fatalf("%s", s.text)
		}
	})
	t.Run("nothing to add leaves the file alone", func(t *testing.T) {
		s := &memStore{text: "profile: p\n"}
		if err := (NodeYAML{Store: s}).AddItems(nil, nil); err != nil || s.text != "profile: p\n" {
			t.Fatalf("%v\n%s", err, s.text)
		}
	})
	t.Run("a list that is not a list is an error, not a guess", func(t *testing.T) {
		s := &memStore{text: "profile_overrides:\n  add_models: nope\n"}
		if err := (NodeYAML{Store: s}).AddItems([]string{"m-a"}, nil); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("the store refusing the file is the caller's error", func(t *testing.T) {
		s := &memStore{text: "profile: p\n", err: errors.New("refused: invalid node.yaml")}
		if err := (NodeYAML{Store: s}).AddItems([]string{"m-a"}, nil); err == nil || !strings.Contains(err.Error(), "refused") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no store", func(t *testing.T) {
		if err := (NodeYAML{}).AddItems([]string{"m-a"}, nil); err == nil {
			t.Fatal("accepted")
		}
	})
}

func TestTheNetworkIsRefusedOrPanicsInTests(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://downloads.example.test/m.gguf", nil)
	if _, err := (NoNetwork{}).RoundTrip(req); !errors.Is(err, ErrNetworkRefused) {
		t.Fatalf("err = %v", err)
	}
	if _, err := Client(false).Do(req); !errors.Is(err, ErrNetworkRefused) {
		t.Fatalf("client err = %v", err)
	}
	if _, err := RefuseDial(false)(context.Background(), "tcp", "downloads.example.test:443"); !errors.Is(err, ErrNetworkRefused) {
		t.Fatalf("dial err = %v", err)
	}
	mustPanic := func(name string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s did not panic", name)
			}
		}()
		f()
	}
	mustPanic("RoundTrip", func() { _, _ = (NoNetwork{Panic: true}).RoundTrip(req) })
	mustPanic("Dial", func() { var c net.Conn; c, _ = RefuseDial(true)(context.Background(), "tcp", "x:1"); _ = c })
}

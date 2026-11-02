package docsrender

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var cardNow = time.Date(2026, 11, 2, 15, 4, 5, 0, time.UTC)

var cardState = NodeState{
	NodeID: "ai-n3", EmergencyNumber: "112", CatalogVersion: "2026.11.02-1",
	Brains: []string{"Model Alpha 9B", "Model Beta 4B"},
}

func cardTemplate(t *testing.T) []byte { return []byte(fixtureDocs["runbooks/00-survival-card.md"]) }

func TestBuildCardFillsInventoryAndNodeState(t *testing.T) {
	html, err := BuildCard(cardTemplate(t), fixturePairs(t), cardState, cardNow)
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	for _, want := range []string{
		"CARD — computer ai-n3", "TARJETA — computadora ai-n3",
		"Log in as <strong>casey</strong>", // <DESKTOP_USER> of this node, not of ai-n1
		"Call <strong>112</strong>",
		"Brains / Cerebros: Model Alpha 9B, Model Beta 4B", // the committed template says <BRAINS_INSTALLED>
		"Catalog / Catálogo 2026.11.02-1",
		"Printed / Impreso 2026-11-02",
		`<link rel="stylesheet" href="card.css">`,
		`<body class="card">`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("card lacks %q", want)
		}
	}
	if strings.Contains(s, "&lt;") {
		t.Errorf("an unfilled placeholder is left on the card: %s", s)
	}
	assertSelfContained(t, "card", html)
}

func TestBuildCardGolden(t *testing.T) {
	html, err := BuildCard(cardTemplate(t), fixturePairs(t), cardState, cardNow)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "survival-card-ai-n3.html", html)
}

func TestBuildCardWithoutInventoryKeepsPlaceholdersAndUsesFallbacks(t *testing.T) {
	st := cardState
	st.DesktopUser = "fallback-user" // from the installer's state.json
	html, err := BuildCard(cardTemplate(t), nil, st, cardNow)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), "<strong>fallback-user</strong>") {
		t.Errorf("no fallback desktop user: %s", html)
	}
	st.DesktopUser = ""
	html, _ = BuildCard(cardTemplate(t), nil, st, cardNow)
	if !strings.Contains(string(html), "<strong>&lt;DESKTOP_USER&gt;</strong>") {
		t.Errorf("an unknown desktop user must stay a visible placeholder: %s", html)
	}
	st.Brains = nil
	html, _ = BuildCard(cardTemplate(t), nil, st, cardNow)
	if !strings.Contains(string(html), "none yet / ninguno todavía") {
		t.Error("no-brains text missing")
	}
}

func TestBuildCardRefusesSecretPlaceholders(t *testing.T) {
	cases := []struct {
		name     string
		extra    string
		pairs    *Pairs
		wantName string
	}{
		{"built-in tailnet IP", "Reach <TAILNET_IP_AI_N3>.", fixturePairs(t), "TAILNET_IP_AI_N3"},
		{"built-in tailnet", "Net <TAILNET>.", fixturePairs(t), "TAILNET"},
		{"built-in zone", "Zone <ROUTE53_ZONE_ID>.", fixturePairs(t), "ROUTE53_ZONE_ID"},
		{"built-in VPS address", "<VPS_PUBLIC_IP>", fixturePairs(t), "VPS_PUBLIC_IP"},
		{"built-in VPS host", "<VPS_HOSTNAME>", fixturePairs(t), "VPS_HOSTNAME"},
		{"built-in LAN IP", "<LAN_IP_AI_N1>", fixturePairs(t), "LAN_IP_AI_N1"},
		{"built-in e-mail", "Write <ADMIN_EMAIL>.", fixturePairs(t), "ADMIN_EMAIL"},
		{"built-in list works with no inventory at all", "<TAILNET_IP_AI_N1>", nil, "TAILNET_IP_AI_N1"},
		// token_label is only on the example's secret_fields list, on no built-in list.
		{"secret_fields of the example", "Label <NODE_TOKEN_LABEL>.", fixturePairs(t), "NODE_TOKEN_LABEL"},
		{"secret_fields field with a drive id", "Drive <ARK_DRIVE_UUID>.", fixturePairs(t), "ARK_DRIVE_UUID"},
	}
	for _, c := range cases {
		tpl := append(cardTemplate(t), []byte("\n"+c.extra+"\n")...)
		html, err := BuildCard(tpl, c.pairs, cardState, cardNow)
		var e *Error
		if !errors.As(err, &e) || e.Kind != KindCardSecret {
			t.Errorf("%s: err = %v", c.name, err)
			continue
		}
		if html != nil {
			t.Errorf("%s: a card was produced", c.name)
		}
		if !reflect.DeepEqual(e.Placeholders, []string{c.wantName}) {
			t.Errorf("%s: placeholders = %v, want [%s]", c.name, e.Placeholders, c.wantName)
		}
		// The message names the placeholder, never a value.
		for _, v := range []string{"192.0.2", "example-tailnet", "person-a", "fixture-uuid", "tl-3"} {
			if strings.Contains(e.Error(), v) {
				t.Errorf("%s: the error leaks the value %q", c.name, v)
			}
		}
	}
}

func TestSecretPlaceholdersAreSortedAndDistinct(t *testing.T) {
	got := SecretPlaceholders([]byte("<TAILNET> <NODE_ID> <ADMIN_EMAIL> <TAILNET>"), nil)
	if !reflect.DeepEqual(got, []string{"ADMIN_EMAIL", "TAILNET"}) {
		t.Errorf("got %v", got)
	}
}

func TestLoadNodeState(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) string {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	nodeYAML := write("etc/node.yaml", "schema_version: 1\nnode:\n  id: ai-n3\nportal:\n  emergency_number: \"112\"\n")
	state := write("var/state.json", `{"phases":{"p1":{"details":{"desktop_user":"casey"}}}}`)
	write("etc/catalog/CATALOG_VERSION", "2026.11.02-1\n")
	write("etc/catalog/models.yaml", "schema_version: 1\nmodels:\n  - id: m-alpha\n    display_name: Model Alpha 9B\n")
	write("srv/models/m-alpha/MANIFEST.json", `{"item_id":"m-alpha","state":"verified","catalog_version":"2026.01.01-1"}`)
	write("srv/models/m-beta/MANIFEST.json", `{"item_id":"m-beta","state":"verified"}`)    // no display name: the id shows
	write("srv/models/m-gone/MANIFEST.json", `{"item_id":"m-gone","state":"quarantined"}`) // not verified: not installed
	write("srv/models/m-bad/MANIFEST.json", `{not json`)
	st := LoadNodeState(StateSources{
		NodeYAML: nodeYAML, InstallState: state,
		ContentRoots: []string{filepath.Join(root, "srv"), filepath.Join(root, "missing")},
		CatalogDirs:  []string{filepath.Join(root, "etc", "catalog")},
	})
	want := NodeState{NodeID: "ai-n3", EmergencyNumber: "112", CatalogVersion: "2026.11.02-1", DesktopUser: "casey",
		Brains: []string{"Model Alpha 9B", "m-beta"}}
	if !reflect.DeepEqual(st, want) {
		t.Errorf("state = %+v\nwant    %+v", st, want)
	}

	// Everything is optional: a damaged node still gets a card.
	empty := LoadNodeState(StateSources{NodeYAML: filepath.Join(root, "none"), InstallState: write("bad.json", "{")})
	if empty.NodeID != "" || empty.EmergencyNumber != DefaultEmergencyNumber || empty.CatalogVersion != "unknown" || len(empty.Brains) != 0 {
		t.Errorf("empty state = %+v", empty)
	}
}

package docsrender

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// The fixture documents are written to a temporary folder by the tests instead of being committed as
// .md files: several of them contain broken links on purpose, and scripts/check-links.sh checks every
// *.md file of the repository. Every name and value is invented.

var update = flag.Bool("update", false, "rewrite the golden files")

var fixtureDocs = map[string]string{
	"README.md": `# Mini docs map

Fixture tree for the renderer tests.

- [Runbooks](runbooks/) and the [runbook index](runbooks/README.md)
- [Specs](specs/) (a folder without a README)
- [First runbook, section "Start here"](runbooks/01-first.md#start-here)
`,
	"runbooks/README.md": `# Mini runbooks

1. [First runbook](01-first.md)
2. [Second runbook](02-second.md)
3. [Survival card](00-survival-card.md)
`,
	"runbooks/00-survival-card.md": `<!--
Card template for the tests. Same shape as the real one: a two-column table and a footer line.
-->

| CARD — computer <NODE_ID> | TARJETA — computadora <NODE_ID> |
|---|---|
| **Start** Log in as **<DESKTOP_USER>**. Open ` + "`127.0.0.1:7070/portal`" + `. | **Inicio** Entre como **<DESKTOP_USER>**. Abra ` + "`127.0.0.1:7070/portal`" + `. |
| **First aid** Call **<EMERGENCY_NUMBER>**. | **Primeros auxilios** Llame al **<EMERGENCY_NUMBER>**. |

Computer / Computadora **<NODE_ID>** · Brains / Cerebros: <BRAINS_INSTALLED> · Catalog / Catálogo <CATALOG_VERSION> · Printed / Impreso <RENDER_DATE>
`,
	"runbooks/01-first.md": "# 01. First runbook: connect\n\n" +
		"Print this page. The one-page version is the [Survival card](00-survival-card.md).\n\n" +
		"## Start here\n\n" +
		"1. Log in as `<DESKTOP_USER>`.\n" +
		"2. Reach node one at <TAILNET_IP_AI_N1> and node three at <TAILNET_IP_AI_N3>.\n\n" +
		"   ```\n   ssh <DESKTOP_USER>@<TAILNET_IP_AI_N1>\n   curl http://<TAILNET_IP_AI_N3>:8080/health\n   ```\n\n" +
		"3. A name no inventory knows stays as it is: <NO_SUCH_VALUE>, and so does <BRAINS>.\n\n" +
		"## Links\n\n" +
		"- Another page: [Mini spec](../specs/mini.md#a-table), and its [top](../specs/mini.md).\n" +
		"- Same page: [Start here](#start-here) and [Links](#links).\n" +
		"- A repository file outside the docs: [the CI script](../../scripts/agent-ci.sh).\n" +
		"- A file inside the docs that is not a page: [data](../specs/data.json).\n" +
		"- A folder: [all specs](../specs/).\n" +
		"- The archive is not rendered: [old notes](../source/old.md).\n" +
		"- External: [the project site](https://example.test/page).\n" +
		"- Unsafe: [click](javascript:alert(1)).\n\n" +
		"## Start here\n\n" +
		"A second heading with the same text gets a numbered anchor: [again](#start-here-1).\n\n" +
		"![a remote picture](https://example.test/pic.png)\n\n" +
		"![a tiny inline picture](data:image/gif;base64,R0lGODlhAQABAAAAACwAAAAAAQABAAA=)\n",
	"runbooks/02-second.md": `---
title_es: Segunda guía
---
# 02. Second runbook

Contact <ADMIN_EMAIL> if the **bold <DESKTOP_USER>** text breaks. See [the missing page](03-missing.md) and [a missing heading](01-first.md#nope).
`,
	"specs/mini.md": `<!-- title_es: Especificación mínima -->
# Spec: mini

## A table

| Name | Value |
|---|:---:|
| alpha | 1 |
| beta | 2 |

- [x] done item
- [ ] open item

~~old~~ text.

<div style="page-break-after: always"></div>

## Raw HTML is dropped

<script>alert("no")</script>

Done.
`,
	"specs/data.json":      `{"note": "a file inside the docs tree that is not a page"}` + "\n",
	"source/old.md":        "# Old notes\n\nThe archive folder is skipped by the renderer.\n",
	"architecture/arch.md": "# Architecture note\n\nBack to the [first runbook](../runbooks/01-first.md).\n",
}

// writeTree writes files (slash paths) under a fresh temporary folder and returns it.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// fixtureSrc returns a docs folder made from fixtureDocs.
func fixtureSrc(t *testing.T) string { return writeTree(t, fixtureDocs) }

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// golden compares got with testdata/golden/<name>; -update rewrites it.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("missing golden file (run go test -update): %v", err)
	}
	if string(want) != string(got) {
		t.Errorf("%s differs from the golden file (run go test -update after checking the diff)\n--- got ---\n%s", name, got)
	}
}

package local

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/audit"
)

func auditLines(t *testing.T, e Env) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(read(t, e.Paths.AuditFile())), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("not JSON: %q", l)
		}
		out = append(out, m)
	}
	return out
}

func TestAuditLineIsTheDocumentedShape(t *testing.T) {
	e, _ := testEnv(t)
	if err := e.Audit(audit.ActionPINSet, audit.ResultOK, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := e.Audit(ActionCatalogUseTest, audit.ResultFail, "LAI-CLI-012", map[string]string{"version": "2026.11.03-1"}); err != nil {
		t.Fatal(err)
	}
	if m := modeOf(t, e.Paths.AuditFile()); m != 0o600 {
		t.Errorf("audit.jsonl mode %o, want 0600", m)
	}
	lines := auditLines(t, e)
	if len(lines) != 2 {
		t.Fatalf("%d lines", len(lines))
	}
	l := lines[0]
	actor := l["actor"].(map[string]any)
	if l["at"] != "2026-11-02T15:04:05Z" || l["action"] != "pin.set" || l["result"] != "ok" || l["code"] != nil ||
		actor["kind"] != "cli-root" || actor["uid"] != float64(0) || actor["via"] != nil || actor["job_id"] != nil || actor["fingerprint"] != nil ||
		l["op_id"] != nil || l["req_id"] != nil || len(l["target"].(map[string]any)) != 0 {
		t.Errorf("line 1: %v", l)
	}
	if lines[1]["action"] != "catalog.use_test" || lines[1]["result"] != "fail" || lines[1]["code"] != "LAI-CLI-012" ||
		lines[1]["target"].(map[string]any)["version"] != "2026.11.03-1" {
		t.Errorf("line 2: %v", lines[1])
	}
}

func TestAuditNeverRotatesOrTruncates(t *testing.T) {
	e, _ := testEnv(t)
	// a file already past the agent's 10 MiB rotation threshold
	line := `{"at":"2026-01-01T00:00:00Z","action":"old","result":"ok"}` + "\n"
	big := bytes.Repeat([]byte(line), int(audit.DefaultMaxSize)/len(line)+10)
	if err := os.WriteFile(e.Paths.AuditFile(), big, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.Audit(audit.ActionTokenRotate, audit.ResultOK, "", nil); err != nil {
		t.Fatal(err)
	}
	if exists(e.Paths.AuditFile() + ".1") {
		t.Error("the CLI rotated audit.jsonl")
	}
	got := read(t, e.Paths.AuditFile())
	if !strings.HasPrefix(got, line) || !strings.HasSuffix(got, "\n") || !strings.Contains(got[len(big):], `"token.rotate"`) {
		t.Error("the old content was not kept, or the line was not appended")
	}
	if len(got) <= len(big) {
		t.Error("nothing appended")
	}
}

func TestAuditAppendsAfterAPartialLine(t *testing.T) {
	// An O_APPEND write after another process's records lands after them, never over them.
	e, _ := testEnv(t)
	for i := 0; i < 3; i++ {
		if err := e.Audit(audit.ActionPINSet, audit.ResultOK, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(auditLines(t, e)); n != 3 {
		t.Errorf("%d lines", n)
	}
}

func TestAuditUnwritableReportsTheAuditCode(t *testing.T) {
	e, _ := testEnv(t)
	e.Paths.StateDir = filepath.Join(e.Paths.StateDir, "missing")
	err := e.Audit(audit.ActionPINSet, audit.ResultOK, "", nil)
	var ae *audit.Error
	if !errors.As(err, &ae) || ae.Code != audit.CodeUnwritable {
		t.Fatalf("got %v", err)
	}
}

func TestLockIsExclusiveAndReleasable(t *testing.T) {
	e, _ := testEnv(t)
	release, err := Lock(e)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(e); !errors.Is(err, ErrBusy) {
		t.Fatalf("second lock: %v, want ErrBusy", err)
	}
	release()
	release2, err := Lock(e)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	release2()
}

func TestLockUnwritableFolder(t *testing.T) {
	e, _ := testEnv(t)
	e.Paths.EtcDir = filepath.Join(e.Paths.EtcDir, "nope")
	if _, err := Lock(e); !isFileErr(err, "it does not exist") {
		t.Fatalf("got %v", err)
	}
}

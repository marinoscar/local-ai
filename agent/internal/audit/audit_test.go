package audit_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/audit/fake"
)

// Fake credentials are built at run time so that no literal in the source matches
// scripts/check-redaction.sh. None of them is real.
func fakeToken() string { return "lat" + "_" + "Ab3dE6gH9jK2mN5pQ8sT1vWx" }

const logPath = "/var/lib/localai/audit.jsonl"

var fixedNow = time.Date(2026, 11, 2, 15, 4, 5, 0, time.UTC)

// slogCapture is a logger writing JSON lines into a buffer (no scrubber: the audit package must
// not depend on the caller's logger to keep secrets out of the mirror).
func slogCapture() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug, ReplaceAttr: func(g []string, a slog.Attr) slog.Attr {
		if len(g) == 0 && a.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return a
	}})
	return slog.New(h), &buf
}

func openFake(t *testing.T, fsys *fake.FS, opts ...audit.Option) (*audit.Log, *bytes.Buffer) {
	t.Helper()
	logger, buf := slogCapture()
	all := append([]audit.Option{audit.WithFS(fsys), audit.WithLogger(logger), audit.WithClock(func() time.Time { return fixedNow })}, opts...)
	l, err := audit.Open(logPath, all...)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return l, buf
}

func lines(data []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

func decode(t *testing.T, line string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("not JSON: %q: %v", line, err)
	}
	return m
}

func content(t *testing.T, fsys *fake.FS, path string) []byte {
	t.Helper()
	b, ok := fsys.Content(path)
	if !ok {
		t.Fatalf("%s does not exist", path)
	}
	return b
}

// ---- the record ---------------------------------------------------------------------------

func TestWrite_RecordShapeMatchesTheSpecExample(t *testing.T) {
	fsys := fake.New()
	l, _ := openFake(t, fsys)
	err := l.Write(audit.Event{
		Action: audit.ActionKeyShow,
		Result: audit.ResultOK,
		Actor:  audit.Operator(audit.ViaBearer).WithCredential("lat" + "_" + "Ab3dE6gH9jK2mN5pQ8sT1v9xQz"),
		Target: map[string]string{"model": ""},
		ReqID:  "req_4f2k9m1q8z0c",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := string(content(t, fsys, logPath))
	want := `{"at":"2026-11-02T15:04:05Z","action":"key.show","result":"ok","code":null,` +
		`"actor":{"kind":"operator","via":"bearer","uid":null,"job_id":null,"fingerprint":"lat_…9xQz"},` +
		`"target":{"model":null},"op_id":null,"req_id":"req_4f2k9m1q8z0c"}` + "\n"
	if raw != want {
		t.Fatalf("line differs from the spec example\n got %s\nwant %s", raw, want)
	}
}

func TestWrite_ActorConstructors(t *testing.T) {
	for name, tc := range map[string]struct {
		actor                    audit.Actor
		kind, via, job           string
		uid                      any
		wantViaNull, wantJobNull bool
	}{
		"anonymous": {actor: audit.Anonymous(), kind: "anonymous", wantViaNull: true, wantJobNull: true},
		"portal":    {actor: audit.Portal(), kind: "portal", via: "portal", wantJobNull: true},
		"operator":  {actor: audit.Operator(audit.ViaSession).WithUID(1000), kind: "operator", via: "session", uid: float64(1000), wantJobNull: true},
		"admin":     {actor: audit.Admin(audit.ViaPIN), kind: "admin", via: "pin", wantJobNull: true},
		"cloud":     {actor: audit.Cloud("job-7"), kind: "cloud", job: "job-7", wantViaNull: true},
		"system":    {actor: audit.System(), kind: "system", wantViaNull: true, wantJobNull: true},
		"cli-root":  {actor: audit.CLIRoot(), kind: "cli-root", uid: float64(0), wantViaNull: true, wantJobNull: true},
	} {
		t.Run(name, func(t *testing.T) {
			fsys := fake.New()
			l, _ := openFake(t, fsys)
			if err := l.Write(audit.Event{Action: audit.ActionModeChange, Result: audit.ResultOK, Actor: tc.actor}); err != nil {
				t.Fatal(err)
			}
			a := decode(t, lines(content(t, fsys, logPath))[0])["actor"].(map[string]any)
			if a["kind"] != tc.kind {
				t.Errorf("kind = %v, want %s", a["kind"], tc.kind)
			}
			if tc.wantViaNull != (a["via"] == nil) || (!tc.wantViaNull && a["via"] != tc.via) {
				t.Errorf("via = %v", a["via"])
			}
			if tc.wantJobNull != (a["job_id"] == nil) || (!tc.wantJobNull && a["job_id"] != tc.job) {
				t.Errorf("job_id = %v", a["job_id"])
			}
			if a["uid"] != tc.uid {
				t.Errorf("uid = %v, want %v", a["uid"], tc.uid)
			}
			if a["fingerprint"] != nil {
				t.Errorf("fingerprint = %v, want null", a["fingerprint"])
			}
		})
	}
}

func TestWrite_TimestampIsUTCSeconds(t *testing.T) {
	fsys := fake.New()
	l, _ := openFake(t, fsys)
	zone := time.FixedZone("x", -6*3600)
	if err := l.Write(audit.Event{At: time.Date(2026, 11, 2, 9, 4, 5, 999_000_000, zone), Action: "a", Result: audit.ResultOK, Actor: audit.System()}); err != nil {
		t.Fatal(err)
	}
	if err := l.Write(audit.Event{Action: "b", Result: audit.ResultOK, Actor: audit.System()}); err != nil {
		t.Fatal(err)
	}
	ls := lines(content(t, fsys, logPath))
	if decode(t, ls[0])["at"] != "2026-11-02T15:04:05Z" || decode(t, ls[1])["at"] != "2026-11-02T15:04:05Z" {
		t.Fatalf("unexpected timestamps: %v", ls)
	}
}

func TestWrite_MissingActionOrResultIsMarkedUnknown(t *testing.T) {
	fsys := fake.New()
	l, _ := openFake(t, fsys)
	if err := l.Write(audit.Event{}); err != nil {
		t.Fatal(err)
	}
	m := decode(t, lines(content(t, fsys, logPath))[0])
	if m["action"] != "unknown" || m["result"] != "unknown" || m["actor"].(map[string]any)["kind"] != "unknown" {
		t.Fatalf("record = %v", m)
	}
}

func TestWrite_SecretsNeverReachTheFileOrTheMirror(t *testing.T) {
	fsys := fake.New()
	l, mirror := openFake(t, fsys)
	tok := fakeToken()
	body := tok[4 : len(tok)-4]
	err := l.Write(audit.Event{
		Action: audit.ActionCloudEnroll, Result: audit.ResultFail, Code: "LAI-CLD-050",
		Actor:  audit.Cloud("job-" + tok).WithCredential(tok),
		Target: map[string]string{"token": tok, "note": "used " + tok, "root": "ark-3f9c0a12"},
		OpID:   "op_" + tok, ReqID: "Bearer " + tok,
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{"file": string(content(t, fsys, logPath)), "mirror": mirror.String()} {
		if strings.Contains(out, body) {
			t.Errorf("secret in the %s: %s", name, out)
		}
	}
	m := decode(t, lines(content(t, fsys, logPath))[0])
	if got := m["actor"].(map[string]any)["fingerprint"]; got != "lat_…"+tok[len(tok)-4:] {
		t.Errorf("fingerprint = %v", got)
	}
	if got := m["target"].(map[string]any)["root"]; got != "ark-3f9c0a12" {
		t.Errorf("an innocent target was damaged: %v", got)
	}
}

func TestWrite_LineNeverExceedsTheLimit(t *testing.T) {
	fsys := fake.New()
	l, _ := openFake(t, fsys)
	huge := strings.Repeat("é", 5000)
	target := map[string]string{}
	for i := 0; i < 60; i++ {
		target[fmt.Sprintf("k%02d", i)] = huge
	}
	if err := l.Write(audit.Event{Action: huge, Result: audit.ResultOK, Code: huge, Actor: audit.Operator(huge), Target: target, OpID: huge, ReqID: huge}); err != nil {
		t.Fatal(err)
	}
	if err := l.Write(audit.Event{Action: "x", Result: audit.ResultOK, Actor: audit.System(), Target: map[string]string{"model": huge}}); err != nil {
		t.Fatal(err)
	}
	raw := content(t, fsys, logPath)
	ls := lines(raw)
	if len(ls) != 2 {
		t.Fatalf("%d lines, want 2", len(ls))
	}
	for _, ln := range ls {
		if len(ln)+1 > audit.MaxLineBytes {
			t.Errorf("a line is %d bytes (limit %d)", len(ln)+1, audit.MaxLineBytes)
		}
		decode(t, ln)
	}
	if !strings.Contains(ls[0], `"truncated"`) {
		t.Errorf("an oversized record should say it was shortened: %s", ls[0])
	}
	// one write(2) per record, even for a shortened one
	if n := fsys.Count("write "); n != 2 {
		t.Errorf("%d write calls for 2 records", n)
	}
}

func TestWrite_MirrorsToSlogWithComponentAudit(t *testing.T) {
	fsys := fake.New()
	l, mirror := openFake(t, fsys)
	if err := l.Write(audit.Event{Action: audit.ActionPINVerifyFail, Result: audit.ResultFail, Code: "LAI-SEC-001", Actor: audit.Admin(audit.ViaPIN).WithUID(0), Target: map[string]string{"mode": "online"}, ReqID: "req_1"}); err != nil {
		t.Fatal(err)
	}
	if err := l.Write(audit.Event{Action: audit.ActionModelLoad, Result: audit.ResultOK, Actor: audit.System(), Target: map[string]string{"model": "qwen3"}}); err != nil {
		t.Fatal(err)
	}
	ls := lines(mirror.Bytes())
	if len(ls) != 2 {
		t.Fatalf("mirror has %d lines: %v", len(ls), ls)
	}
	m := decode(t, ls[0])
	if m["component"] != "audit" || m["msg"] != "audit" || m["action"] != "pin.verify.fail" || m["result"] != "fail" || m["code"] != "LAI-SEC-001" || m["req_id"] != "req_1" {
		t.Errorf("mirror line = %v", m)
	}
	if m["level"] != "WARN" {
		t.Errorf("a failed action should mirror at warn, got %v", m["level"])
	}
	if a := m["actor"].(map[string]any); a["kind"] != "admin" || a["via"] != "pin" || a["uid"] != float64(0) {
		t.Errorf("actor = %v", a)
	}
	if tg := m["target"].(map[string]any); tg["mode"] != "online" {
		t.Errorf("target = %v", tg)
	}
	if m2 := decode(t, ls[1]); m2["level"] != "INFO" || m2["target"].(map[string]any)["model"] != "qwen3" {
		t.Errorf("second mirror line = %v", m2)
	}
}

// ---- fsync, one write per line, file mode -------------------------------------------------

func TestWrite_EveryLineIsOneWriteFollowedByAnFsync(t *testing.T) {
	fsys := fake.New()
	l, _ := openFake(t, fsys)
	for i := 0; i < 5; i++ {
		if err := l.Write(audit.Event{Action: "a", Result: audit.ResultOK, Actor: audit.System(), ReqID: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if w, s := fsys.Count("write "), fsys.Count("sync "); w != 5 || s != 5 {
		t.Fatalf("writes=%d syncs=%d, want 5 and 5", w, s)
	}
	// each write is immediately followed by the sync of the same file
	ops := fsys.Ops()
	for i, op := range ops {
		if strings.HasPrefix(op, "write ") {
			if i+1 >= len(ops) || ops[i+1] != "sync "+logPath {
				t.Fatalf("write at op %d is not followed by an fsync: %v", i, ops)
			}
		}
	}
	if n := len(lines(content(t, fsys, logPath))); n != 5 {
		t.Fatalf("%d lines in the file", n)
	}
}

func TestOpen_CreatesTheFileWith0600AndFixesALooserMode(t *testing.T) {
	fsys := fake.New()
	openFake(t, fsys)
	if m, ok := fsys.Mode(logPath); !ok || m != 0o600 {
		t.Fatalf("mode = %v (exists %v), want 0600", m, ok)
	}

	loose := fake.New()
	loose.Put(logPath, []byte("old line\n"), 0o644)
	openFake(t, loose)
	if m, _ := loose.Mode(logPath); m != 0o600 {
		t.Fatalf("existing file mode = %v, want 0600", m)
	}
	if string(content(t, loose, logPath)) != "old line\n" {
		t.Fatal("opening must never truncate the file")
	}
}

func TestWrite_AppendsToAnExistingFile(t *testing.T) {
	fsys := fake.New()
	fsys.Put(logPath, []byte("{\"old\":1}\n"), 0o600)
	l, _ := openFake(t, fsys)
	if err := l.Write(audit.Event{Action: "a", Result: audit.ResultOK, Actor: audit.System()}); err != nil {
		t.Fatal(err)
	}
	ls := lines(content(t, fsys, logPath))
	if len(ls) != 2 || ls[0] != `{"old":1}` {
		t.Fatalf("lines = %v", ls)
	}
}

// ---- rotation -----------------------------------------------------------------------------

func seqOf(t *testing.T, line string) int {
	t.Helper()
	var n int
	if _, err := fmt.Sscanf(decode(t, line)["req_id"].(string), "req_%d", &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRotation_KeepsExactlyFiveOldFilesNewestFirst(t *testing.T) {
	fsys := fake.New()
	const max = 600
	l, _ := openFake(t, fsys, audit.WithMaxSize(max))
	const total = 120
	for i := 0; i < total; i++ {
		if err := l.Write(audit.Event{Action: "a", Result: audit.ResultOK, Actor: audit.System(), ReqID: fmt.Sprintf("req_%04d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{logPath, logPath + ".1", logPath + ".2", logPath + ".3", logPath + ".4", logPath + ".5"}
	got := fsys.Names()
	if strings.Join(got, " ") != strings.Join(sorted(want), " ") {
		t.Fatalf("files = %v, want exactly %v", got, sorted(want))
	}
	// current, .1 (newest) ... .5 (oldest): the sequence numbers run backwards without a gap
	next := total - 1
	for _, p := range want {
		ls := lines(content(t, fsys, p))
		if len(ls) == 0 {
			t.Fatalf("%s is empty", p)
		}
		if size := len(content(t, fsys, p)); size > max {
			t.Errorf("%s is %d bytes, above the %d threshold", p, size, max)
		}
		for i := len(ls) - 1; i >= 0; i-- {
			if got := seqOf(t, ls[i]); got != next {
				t.Fatalf("%s line %d has sequence %d, want %d", p, i, got, next)
			}
			next--
		}
	}
	if next < 0 {
		t.Fatal("test wrote too few lines to drop anything")
	}
	if _, ok := fsys.Content(logPath + ".6"); ok {
		t.Fatal("a .6 file exists")
	}
	// rotated files keep their private mode
	for _, p := range want {
		if m, _ := fsys.Mode(p); m != 0o600 {
			t.Errorf("%s mode = %v", p, m)
		}
	}
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestRotation_DefaultThresholdIs10MiB(t *testing.T) {
	if audit.DefaultMaxSize != 10<<20 {
		t.Fatalf("DefaultMaxSize = %d", audit.DefaultMaxSize)
	}
	fsys := fake.New()
	fsys.Put(logPath, bytes.Repeat([]byte("x"), 10<<20-50), 0o600)
	l, _ := openFake(t, fsys)
	// the first record does not fit under 10 MiB: the file rotates before it is written
	if err := l.Write(audit.Event{Action: "a", Result: audit.ResultOK, Actor: audit.System()}); err != nil {
		t.Fatal(err)
	}
	if old, ok := fsys.Content(logPath + ".1"); !ok || len(old) != 10<<20-50 {
		t.Fatalf(".1 missing or wrong size (%d)", len(old))
	}
	if cur := content(t, fsys, logPath); len(lines(cur)) != 1 {
		t.Fatalf("the new file should hold only the new record: %q", cur)
	}
}

func TestRotation_RemovesDotFiveBeforeShifting(t *testing.T) {
	fsys := fake.New()
	for i := 1; i <= 5; i++ {
		fsys.Put(fmt.Sprintf("%s.%d", logPath, i), []byte(fmt.Sprintf("old%d\n", i)), 0o600)
	}
	fsys.Put(logPath, bytes.Repeat([]byte("x"), 100), 0o600)
	l, _ := openFake(t, fsys, audit.WithMaxSize(100))
	if err := l.Write(audit.Event{Action: "a", Result: audit.ResultOK, Actor: audit.System()}); err != nil {
		t.Fatal(err)
	}
	var shift []string
	for _, op := range fsys.Ops() {
		if strings.HasPrefix(op, "remove ") || strings.HasPrefix(op, "rename ") {
			shift = append(shift, strings.ReplaceAll(op, logPath, "A"))
		}
	}
	want := "remove A.5|rename A.4 A.5|rename A.3 A.4|rename A.2 A.3|rename A.1 A.2|rename A A.1"
	if strings.Join(shift, "|") != want {
		t.Fatalf("rotation steps:\n got %s\nwant %s", strings.Join(shift, "|"), want)
	}
	if b := content(t, fsys, logPath+".5"); string(b) != "old4\n" {
		t.Fatalf(".5 = %q, want the former .4", b)
	}
}

func TestRotation_FailureIsNotFatalAndBacksOff(t *testing.T) {
	fsys := fake.New()
	fsys.Put(logPath, bytes.Repeat([]byte("x\n"), 100), 0o600)
	fsys.RenameErr = func(string) error { return syscall.EROFS }
	now := fixedNow
	logger, buf := slogCapture()
	l, err := audit.Open(logPath, audit.WithFS(fsys), audit.WithLogger(logger), audit.WithMaxSize(100), audit.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := l.Write(audit.Event{Action: "a", Result: audit.ResultOK, Actor: audit.System()}); err != nil {
			t.Fatalf("write %d must succeed although rotation fails: %v", i, err)
		}
	}
	if n := len(lines(content(t, fsys, logPath))); n != 100+3 {
		t.Fatalf("%d lines, records were lost", n)
	}
	// one attempt only: the others fall inside the back-off
	if n := fsys.Count("remove "); n != 1 {
		t.Errorf("%d rotation attempts, want 1 within the back-off", n)
	}
	if !strings.Contains(buf.String(), "rotation failed") || !strings.Contains(buf.String(), audit.CodeUnwritable) {
		t.Errorf("no warning with the doctor code: %s", buf.String())
	}
	// after the back-off it tries again, and succeeds once the cause is gone
	fsys.RenameErr = nil
	now = now.Add(2 * time.Minute)
	if err := l.Write(audit.Event{Action: "a", Result: audit.ResultOK, Actor: audit.System()}); err != nil {
		t.Fatal(err)
	}
	if _, ok := fsys.Content(logPath + ".1"); !ok {
		t.Fatal("rotation did not happen after the back-off")
	}
}

func TestWithoutRotation_NeverRenamesOrRemoves(t *testing.T) {
	fsys := fake.New()
	fsys.Put(logPath, bytes.Repeat([]byte("x"), 1000), 0o600)
	l, _ := openFake(t, fsys, audit.WithMaxSize(10), audit.WithoutRotation())
	for i := 0; i < 5; i++ {
		if err := l.Write(audit.Event{Action: "a", Result: audit.ResultOK, Actor: audit.CLIRoot()}); err != nil {
			t.Fatal(err)
		}
	}
	if fsys.Count("rename ") != 0 || fsys.Count("remove ") != 0 {
		t.Fatalf("a CLI writer touched file names: %v", fsys.Ops())
	}
	if names := fsys.Names(); len(names) != 1 {
		t.Fatalf("files = %v", names)
	}
}

// ---- failure paths ------------------------------------------------------------------------

func TestOpen_FailureReportsTheDoctorCodeAndKeepsLoggingToSlog(t *testing.T) {
	fsys := fake.New()
	fsys.OpenErr = func(string) error { return syscall.EROFS }
	logger, buf := slogCapture()
	l, err := audit.Open(logPath, audit.WithFS(fsys), audit.WithLogger(logger), audit.WithClock(func() time.Time { return fixedNow }))
	if l == nil {
		t.Fatal("Open must return a usable Log even when it fails")
	}
	var ae *audit.Error
	if !errors.As(err, &ae) || ae.Code != "LAI-AGT-013" || !errors.Is(err, syscall.EROFS) {
		t.Fatalf("err = %v, want *audit.Error with code LAI-AGT-013 wrapping EROFS", err)
	}
	if got := l.Err(); got == nil {
		t.Fatal("Err() is nil on a degraded log")
	}

	werr := l.Write(audit.Event{Action: audit.ActionKeyShow, Result: audit.ResultOK, Actor: audit.Operator(audit.ViaBearer), ReqID: "req_x"})
	if !errors.As(werr, &ae) || ae.Code != audit.CodeUnwritable {
		t.Fatalf("Write error = %v", werr)
	}
	// the record is in the journal even though the file is unusable
	var mirrored bool
	for _, ln := range lines(buf.Bytes()) {
		m := decode(t, ln)
		if m["msg"] == "audit" && m["action"] == "key.show" && m["req_id"] == "req_x" && m["component"] == "audit" {
			mirrored = true
		}
	}
	if !mirrored {
		t.Fatalf("the record did not reach slog:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "LAI-AGT-013") {
		t.Fatalf("no warning carrying the doctor code:\n%s", buf.String())
	}
	if _, ok := fsys.Content(logPath); ok {
		t.Fatal("a file was created despite the open failure")
	}
}

func TestOpen_RealFilesystemUnwritableDirectory(t *testing.T) {
	// A path below a regular file cannot be opened, for root and for ordinary users alike.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	logger, buf := slogCapture()
	l, err := audit.Open(filepath.Join(blocker, "audit.jsonl"), audit.WithLogger(logger))
	var ae *audit.Error
	if !errors.As(err, &ae) || ae.Code != "LAI-AGT-013" {
		t.Fatalf("err = %v", err)
	}
	if werr := l.Write(audit.Event{Action: "a", Result: audit.ResultOK, Actor: audit.System()}); werr == nil {
		t.Fatal("Write must report the failure")
	}
	if !strings.Contains(buf.String(), `"action":"a"`) {
		t.Fatalf("not mirrored: %s", buf.String())
	}
}

func TestWrite_RecoversWhenTheCauseIsGone(t *testing.T) {
	fsys := fake.New()
	failing := true
	fsys.OpenErr = func(string) error {
		if failing {
			return syscall.ENOSPC
		}
		return nil
	}
	logger, buf := slogCapture()
	l, _ := audit.Open(logPath, audit.WithFS(fsys), audit.WithLogger(logger), audit.WithClock(func() time.Time { return fixedNow }))
	if l.Write(audit.Event{Action: "a", Result: audit.ResultOK, Actor: audit.System()}) == nil {
		t.Fatal("expected a failure")
	}
	failing = false
	if err := l.Write(audit.Event{Action: "b", Result: audit.ResultOK, Actor: audit.System()}); err != nil {
		t.Fatalf("the log should reopen the file by itself: %v", err)
	}
	if l.Err() != nil {
		t.Fatalf("Err() = %v after recovery", l.Err())
	}
	if ls := lines(content(t, fsys, logPath)); len(ls) != 1 || decode(t, ls[0])["action"] != "b" {
		t.Fatalf("file = %v", ls)
	}
	if !strings.Contains(buf.String(), "writable again") {
		t.Fatalf("recovery not logged: %s", buf.String())
	}
}

func TestWrite_DiskFullKeepsTheJournalAndNeverGluesRecords(t *testing.T) {
	fsys := fake.New()
	l, buf := openFake(t, fsys)
	ev := func(i int) audit.Event {
		return audit.Event{Action: "a", Result: audit.ResultOK, Actor: audit.System(), ReqID: fmt.Sprintf("req_%d", i)}
	}
	if err := l.Write(ev(1)); err != nil {
		t.Fatal(err)
	}

	fsys.DiskFree = 30 // room for a fragment of the next line only
	err := l.Write(ev(2))
	var ae *audit.Error
	if !errors.As(err, &ae) || ae.Code != audit.CodeUnwritable || !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("err = %v, want LAI-AGT-013 wrapping ENOSPC", err)
	}
	if l.Err() == nil {
		t.Fatal("Err() should report the failure")
	}
	if !strings.Contains(buf.String(), `"req_id":"req_2"`) {
		t.Fatal("the record that could not be written must still be in the journal")
	}
	if l.Write(ev(3)) == nil {
		t.Fatal("still no space: expected another failure")
	}

	fsys.DiskFree = -1
	if err := l.Write(ev(4)); err != nil {
		t.Fatalf("space is back: %v", err)
	}
	if l.Err() != nil {
		t.Fatal("Err() should be nil again")
	}
	// Records 1 and 4 are intact and on lines of their own; only fragments are not valid JSON.
	var valid []int
	for _, ln := range lines(content(t, fsys, logPath)) {
		var m map[string]any
		if json.Unmarshal([]byte(ln), &m) == nil {
			valid = append(valid, seqOf(t, ln))
		}
	}
	if fmt.Sprint(valid) != "[1 4]" {
		t.Fatalf("valid records = %v, want [1 4]", valid)
	}
}

func TestWrite_FsyncFailureIsReportedAndRecovers(t *testing.T) {
	fsys := fake.New()
	l, _ := openFake(t, fsys)
	fsys.SyncErr = syscall.EIO
	err := l.Write(audit.Event{Action: "a", Result: audit.ResultOK, Actor: audit.System()})
	var ae *audit.Error
	if !errors.As(err, &ae) || ae.Op != "sync" || ae.Code != audit.CodeUnwritable {
		t.Fatalf("err = %v", err)
	}
	fsys.SyncErr = nil
	if err := l.Write(audit.Event{Action: "b", Result: audit.ResultOK, Actor: audit.System()}); err != nil {
		t.Fatal(err)
	}
	if l.Err() != nil {
		t.Fatal("Err() should be nil after a good write")
	}
}

func TestClose(t *testing.T) {
	fsys := fake.New()
	l, buf := openFake(t, fsys)
	if err := l.Write(audit.Event{Action: "a", Result: audit.ResultOK, Actor: audit.System()}); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := l.Write(audit.Event{Action: "late", Result: audit.ResultOK, Actor: audit.System()}); !errors.Is(err, audit.ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
	if !strings.Contains(buf.String(), `"action":"late"`) {
		t.Fatal("a record written after Close must still reach the journal")
	}
	if n := len(lines(content(t, fsys, logPath))); n != 1 {
		t.Fatalf("%d lines", n)
	}
}

// ---- the real filesystem ------------------------------------------------------------------

func TestRealFilesystem_CreatesWith0600AppendsAndRotates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	logger, _ := slogCapture()
	l, err := audit.Open(path, audit.WithLogger(logger), audit.WithMaxSize(2000))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("stat: %v, %v", fi, err)
	}
	for i := 0; i < 100; i++ {
		if err := l.Write(audit.Event{Action: "a", Result: audit.ResultOK, Actor: audit.System(), ReqID: fmt.Sprintf("req_%04d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"audit.jsonl", "audit.jsonl.1", "audit.jsonl.2", "audit.jsonl.3", "audit.jsonl.4", "audit.jsonl.5"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if fi.Mode().Perm() != 0o600 || fi.Size() > 2000 {
			t.Errorf("%s: mode %v size %d", name, fi.Mode().Perm(), fi.Size())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "audit.jsonl.6")); !os.IsNotExist(err) {
		t.Fatalf(".6 exists or unexpected error: %v", err)
	}
}

// An agent (rotating) and an in-process CLI writer (not rotating) append to the same file at the
// same time: no line is lost or torn.
func TestRealFilesystem_TwoWritersNeverInterleave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	logger, _ := slogCapture()
	agent, err := audit.Open(path, audit.WithLogger(logger))
	if err != nil {
		t.Fatal(err)
	}
	cli, err := audit.Open(path, audit.WithLogger(logger), audit.WithoutRotation())
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	defer cli.Close()

	const workers, each = 6, 80
	pad := strings.Repeat("p", 200)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		l := agent
		if w%2 == 1 {
			l = cli
		}
		wg.Add(1)
		go func(w int, l *audit.Log) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if err := l.Write(audit.Event{Action: "a", Result: audit.ResultOK, Actor: audit.System(), Target: map[string]string{"note": pad}, ReqID: fmt.Sprintf("req_%d", w*1000+i)}); err != nil {
					t.Error(err)
					return
				}
			}
		}(w, l)
	}
	wg.Wait()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ls := lines(raw)
	if len(ls) != workers*each {
		t.Fatalf("%d lines, want %d", len(ls), workers*each)
	}
	seen := map[string]bool{}
	for _, ln := range ls {
		m := decode(t, ln)
		seen[m["req_id"].(string)] = true
	}
	if len(seen) != workers*each {
		t.Fatalf("%d distinct records, want %d", len(seen), workers*each)
	}
}

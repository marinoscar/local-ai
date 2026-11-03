package ark_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	apispec "github.com/marinoscar/local-ai/agent/api"
	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// listDetector is a Detector over a fixed list of drives.
type listDetector struct{ drives []ark.Drive }

func (l listDetector) Drives() []ark.Drive { return l.drives }
func (l listDetector) Drive(id string) (ark.Drive, bool) {
	for _, d := range l.drives {
		if d.RootID == id {
			return d, true
		}
	}
	return ark.Drive{}, false
}
func (l listDetector) ActiveRoots() []ark.Root          { return nil }
func (l listDetector) Subscribe() <-chan ark.DriveEvent { return nil }
func (l listDetector) OnRemoved(func(string))           {}
func (l listDetector) Refresh(context.Context) error    { return nil }

func prob(code string, details map[string]any) ark.Problem {
	sev := doctor.Warning
	if c, ok := doctor.Lookup(code); ok {
		sev = c.Severity
	}
	return ark.Problem{Code: code, Severity: sev, Details: details}
}

// specExample is the drives of the example of ark-sync.md section 3.5, plus a drive that is not
// mounted and a damaged one.
func specExample() []ark.Drive {
	return []ark.Drive{
		{RootID: "ark-3f9c0a12", Device: "/dev/sdb1", Mount: "/media/operator/AI-ARK", FSType: "ext4", Label: "AI-ARK",
			SizeBytes: 2000398934016, FreeBytes: 812345678901, State: ark.StateReady, Initialized: true, Profile: "ark-drive-full", MountID: 120},
		{Device: "/dev/sdc1", Mount: "/media/operator/AI-ARK1", FSType: "exfat", Label: "AI-ARK", State: ark.StateUnusable,
			SizeBytes: 31914983424, FreeBytes: 31914000000,
			Problems: []ark.Problem{prob(doctor.ARK002, map[string]any{"device": "/dev/sdc1"})}},
		{RootID: "ark-b81d44e0", Device: "/dev/sdd1", FSType: "ext4", Label: "AI-ARK", State: ark.StateNotMounted,
			Problems: []ark.Problem{prob(doctor.ARK005, nil)}},
		{RootID: "ark-0a0a0a0a", Device: "/dev/sde1", Mount: "/media/operator/AI-ARK2", FSType: "ext4", Label: "AI-ARK", State: ark.StateUnclean,
			SizeBytes: 1000, FreeBytes: 10, FSErrors: 3, ReadOnly: true, Initialized: true, Profile: "ark-drive-full",
			Problems: []ark.Problem{prob(doctor.ARK004, map[string]any{"errors_count": 3})}},
		{RootID: "ark-c0c0c0c0", Device: "/dev/sdf1", Mount: "/media/operator/AI-ARK3", FSType: "ext4", Label: "AI-ARK", State: ark.StateUnusable,
			Detail: "ROOT.json is damaged: invalid JSON"},
	}
}

func render(t *testing.T, v ark.DrivesView, lang string) ([]any, string) {
	t.Helper()
	list, err := v.Drives(context.Background(), lang)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(map[string]any{"drives": list})
	if err != nil {
		t.Fatal(err)
	}
	return list, string(b)
}

// TestDrivesBodyMatchesTheOpenAPISchema: GET /ark/drives validates against the ArkDrives schema of
// openapi.yaml, in both languages and for every kind of drive.
func TestDrivesBodyMatchesTheOpenAPISchema(t *testing.T) {
	spec, err := apispec.Default()
	if err != nil {
		t.Fatal(err)
	}
	sch, err := spec.Schema("ArkDrives")
	if err != nil {
		t.Fatal(err)
	}
	v := ark.DrivesView{
		Detector: listDetector{specExample()},
		Ready: func(id string) (ark.ReadySummary, bool) {
			if id == "ark-3f9c0a12" {
				return ark.ReadySummary{Overall: "yellow", GeneratedAt: "2026-11-01T04:02:10Z"}, true
			}
			return ark.ReadySummary{}, false
		},
	}
	for _, lang := range []string{"en", "es"} {
		_, body := render(t, v, lang)
		var doc any
		if err := json.Unmarshal([]byte(body), &doc); err != nil {
			t.Fatal(err)
		}
		if err := spec.V.Validate(sch, doc); err != nil {
			t.Errorf("%s: %v\n%s", lang, err, body)
		}
	}
	// The schema is not vacuous: a drive without a state is refused.
	var bad any
	_ = json.Unmarshal([]byte(`{"drives":[{"root_id":null,"device":"/dev/sdb1","mount":null}]}`), &bad)
	if err := spec.V.Validate(sch, bad); err == nil {
		t.Error("the ArkDrives schema accepted a drive with no state")
	}
	for _, empty := range []ark.DrivesView{{Detector: listDetector{}}} {
		_, body := render(t, empty, "en")
		if body != `{"drives":[]}` {
			t.Errorf("no drives = %s", body)
		}
	}
}

func decodeDrives(t *testing.T, body string) []map[string]any {
	t.Helper()
	var doc struct {
		Drives []map[string]any `json:"drives"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Drives
}

func TestDrivesShapeOfEachKind(t *testing.T) {
	v := ark.DrivesView{
		Detector: listDetector{specExample()},
		Ready: func(id string) (ark.ReadySummary, bool) {
			return ark.ReadySummary{Overall: "yellow", GeneratedAt: "2026-11-01T04:02:10Z"}, true
		},
	}
	_, body := render(t, v, "en")
	ds := decodeDrives(t, body)
	if len(ds) != 5 {
		t.Fatalf("drives = %d", len(ds))
	}

	ready := ds[0]
	for k, want := range map[string]any{
		"root_id": "ark-3f9c0a12", "device": "/dev/sdb1", "mount": "/media/operator/AI-ARK", "fstype": "ext4", "label": "AI-ARK",
		"size_bytes": float64(2000398934016), "free_bytes": float64(812345678901), "state": "ready", "read_only": false,
		"fs_errors": float64(0), "initialized": true, "profile": "ark-drive-full",
	} {
		if ready[k] != want {
			t.Errorf("ready.%s = %v (%T), want %v", k, ready[k], ready[k], want)
		}
	}
	if r, _ := ready["ready"].(map[string]any); r["overall"] != "yellow" || r["generated_at"] != "2026-11-01T04:02:10Z" {
		t.Errorf("ready.ready = %v", ready["ready"])
	}
	if p, ok := ready["problems"].([]any); !ok || len(p) != 0 {
		t.Errorf("a drive with no problem has an empty list, got %v", ready["problems"])
	}

	exfat := ds[1]
	if exfat["root_id"] != nil || exfat["state"] != "unusable" || exfat["fstype"] != "exfat" || exfat["profile"] != nil || exfat["ready"] != nil || exfat["initialized"] != false {
		t.Errorf("exfat = %v", exfat)
	}
	p := exfat["problems"].([]any)[0].(map[string]any)
	if p["code"] != "LAI-ARK-002" || p["severity"] != "error" || p["title"] != "A drive labelled AI-ARK is not ext4" || p["fix"] == "" {
		t.Errorf("problem = %v", p)
	}

	notMounted := ds[2]
	if _, has := notMounted["mount"]; !has || notMounted["mount"] != nil {
		t.Errorf("a drive that is not mounted has mount: null, got %v", notMounted)
	}
	if _, has := notMounted["size_bytes"]; has {
		t.Errorf("sizes of a drive that is not mounted: %v", notMounted)
	}
	if notMounted["root_id"] != "ark-b81d44e0" || notMounted["state"] != "not_mounted" {
		t.Errorf("not mounted = %v", notMounted)
	}

	unclean := ds[3]
	if unclean["state"] != "unclean" || unclean["fs_errors"] != float64(3) || unclean["read_only"] != true {
		t.Errorf("unclean = %v", unclean)
	}
	// An unclean drive is in the active set, so it carries a ready summary when READY has one.
	if unclean["ready"] == nil {
		t.Error("an active drive gets its ready summary")
	}
	// An uninitialized or unusable drive does not, whatever the lookup says.
	if ds[1]["ready"] != nil || ds[2]["ready"] != nil || ds[4]["ready"] != nil {
		t.Error("only a drive in the active set has a ready summary")
	}
	if ds[4]["detail"] != "ROOT.json is damaged: invalid JSON" {
		t.Errorf("detail = %v", ds[4]["detail"])
	}
}

func TestDrivesWithoutAReadyLookupHaveNullReady(t *testing.T) {
	_, body := render(t, ark.DrivesView{Detector: listDetector{specExample()[:1]}}, "en")
	if d := decodeDrives(t, body)[0]; d["ready"] != nil {
		t.Errorf("ready = %v", d["ready"])
	}
	if !strings.Contains(body, `"ready":null`) {
		t.Errorf("ready must be present as null: %s", body)
	}
}

func TestDrivesProblemTextsFollowTheLanguageAndNameTheDevice(t *testing.T) {
	v := ark.DrivesView{Detector: listDetector{specExample()}}
	for lang, wantTitle := range map[string]string{
		"en": "The ark drive's filesystem has errors",
		"es": "El sistema de archivos de la unidad del arca tiene errores",
	} {
		_, body := render(t, v, lang)
		p := decodeDrives(t, body)[3]["problems"].([]any)[0].(map[string]any)
		if p["title"] != wantTitle {
			t.Errorf("%s title = %v", lang, p["title"])
		}
		fix, _ := p["fix"].(string)
		if !strings.Contains(fix, "fsck.ext4 -f /dev/sde1") || strings.Contains(fix, "<device>") || strings.Contains(fix, "<dispositivo>") {
			t.Errorf("%s fix = %q", lang, fix)
		}
		if det, _ := p["details"].(map[string]any); det["errors_count"] != float64(3) {
			t.Errorf("details = %v", p["details"])
		}
	}
	// Anything but es is English.
	_, fr := render(t, v, "fr")
	_, en := render(t, v, "en")
	if fr != en {
		t.Error("an unknown language must give English")
	}
}

func TestDrivesViewNeverExposesTheUUID(t *testing.T) {
	d := ark.Drive{RootID: "ark-3f9c0a12", Device: "/dev/sdb1", Mount: "/m", UUID: uuidA, State: ark.StateReady}
	_, body := render(t, ark.DrivesView{Detector: listDetector{[]ark.Drive{d}}}, "en")
	if strings.Contains(body, uuidA) || strings.Contains(strings.ToLower(body), "uuid") {
		t.Errorf("the filesystem UUID leaked: %s", body)
	}
}

// TestDrivesViewOverTheRealDetector: the whole path, from a fixture mount table to the JSON.
func TestDrivesViewOverTheRealDetector(t *testing.T) {
	r := newRig(t)
	mnt := r.dir("AI-ARK")
	r.initDrive(mnt, uuidA, "")
	r.mounts.set(r.rootDisk(), r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4"))
	r.refresh()
	v := ark.DrivesView{Detector: r.m}
	_, body := render(t, v, "en")
	d := decodeDrives(t, body)
	if len(d) != 1 || d[0]["state"] != "ready" || d[0]["root_id"] != "ark-3f9c0a12" || d[0]["mount"] != mnt || d[0]["size_bytes"] != float64(2000) {
		t.Errorf("body = %s", body)
	}
}

package places

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

func TestFold(t *testing.T) {
	tests := []struct{ in, want string }{
		{"San José", "san jose"},
		{"LIMÓN", "limon"},
		{"Nicaragüense", "nicaraguense"},
		{"Año Nuevo", "ano nuevo"},
		{"  Río--Grande,  (Alto) ", "rio grande alto"},
		{"Ångström", "angstrom"},
		{"Łódź", "lodz"},
		{"Straße", "strasse"},
		{"Œuvre Æther", "oeuvre aether"},
		{"Сан-Хосе", "сан хосе"},
		{"", ""},
		{"!!!", ""},
		{"a\tb\nc", "a b c"},
	}
	for _, tc := range tests {
		if got := Fold(tc.in); got != tc.want {
			t.Errorf("Fold(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseRow(t *testing.T) {
	good := geo(1, "Name", "Name", "a,b,c,d,e,f,g,Name", 9.5, -84.5, "P", "PPL", "CR", "08", 1234)
	badCols := strings.Join(strings.Split(good, "\t")[:18], "\t")
	tests := []struct {
		name string
		line string
		ok   bool
	}{
		{"valid", good, true},
		{"crlf", good + "\r\n", true},
		{"18 columns", badCols, false},
		{"bad id", strings.Replace(good, "1\tName", "x\tName", 1), false},
		{"latitude out of range", geo(1, "N", "N", "", 95, 0, "P", "PPL", "CR", "", 0), false},
		{"bad population", strings.Replace(good, "1234", "12x4", 1), false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := ParseRow(tc.line)
			if ok != tc.ok {
				t.Fatalf("ok = %v", ok)
			}
		})
	}
	r, _ := ParseRow(good)
	if r.ID != 1 || r.Lat != 9.5 || r.Lon != -84.5 || r.Class != "P" || r.Code != "PPL" || r.Country != "CR" || r.Admin1Code != "08" || r.Population != 1234 {
		t.Errorf("%+v", r)
	}
	if len(r.Alternates) != MaxAlternates {
		t.Errorf("alternates capped at %d, got %v", MaxAlternates, r.Alternates)
	}
}

func TestKindOf(t *testing.T) {
	tests := []struct {
		class, code string
		pop         uint32
		want        string
	}{
		{"S", "HSP", 0, KindHospital},
		{"S", "HSPC", 0, KindClinic},
		{"S", "HSPD", 0, KindClinic},
		{"H", "SPNG", 0, KindWater}, {"H", "WLL", 0, KindWater}, {"H", "RSV", 0, KindWater},
		{"H", "LK", 0, KindWater}, {"H", "STM", 0, KindWater},
		{"S", "SCH", 0, KindSchool},
		{"S", "PP", 0, KindPolice},
		{"S", "AIRP", 0, KindAirport},
		{"T", "MT", 0, KindMountain}, {"T", "VLC", 0, KindMountain},
		{"T", "PK", 0, KindPark}, {"L", "PRK", 0, KindPark},
		{"P", "PPLC", 10, KindCity},
		{"P", "PPLA", 10, KindCity},
		{"P", "PPLA2", 10, KindTown},
		{"P", "PPL", 100000, KindCity},
		{"P", "PPL", 99999, KindTown},
		{"A", "ADM1", 500000, KindPlace},
		{"S", "CH", 0, KindPlace},
		{"S", "BDG", 0, KindPlace},
	}
	for _, tc := range tests {
		if got := KindOf(tc.class, tc.code, tc.pop); got != tc.want {
			t.Errorf("KindOf(%s,%s,%d) = %s, want %s", tc.class, tc.code, tc.pop, got, tc.want)
		}
	}
	for _, k := range Kinds {
		if !ValidKind(k) {
			t.Errorf("%s not valid", k)
		}
	}
	if ValidKind("beach") {
		t.Error("beach is not a kind")
	}
}

func TestKeep(t *testing.T) {
	tests := []struct {
		class, code string
		want        bool
	}{
		{"P", "PPL", true}, {"P", "PPLX", true},
		{"A", "ADM1", true}, {"A", "ADM2", true}, {"A", "ADM3", false}, {"A", "PCLI", false},
		{"S", "HSP", true}, {"S", "SCH", true}, {"S", "BDG", true}, {"H", "STM", true},
		{"T", "HLL", false}, {"S", "FRM", false},
	}
	for _, tc := range tests {
		if got := Keep(tc.class, tc.code); got != tc.want {
			t.Errorf("Keep(%s,%s) = %v", tc.class, tc.code, got)
		}
	}
}

func TestBuildFilterAndFields(t *testing.T) {
	ix := buildCR(t)
	if ix.Len() != len(crRows())-1 { // the hill is dropped
		t.Fatalf("Len = %d, want %d", ix.Len(), len(crRows())-1)
	}
	if ix.TotalRows() != len(crRows()) {
		t.Fatalf("TotalRows = %d", ix.TotalRows())
	}
	rs := ix.Search("Hospital San Juan", nil, 1)
	if len(rs) != 1 || rs[0].Admin1 != "Provincia de San José" && rs[0].Admin1 != "San José" {
		t.Fatalf("%+v", rs)
	}
	if rs[0].Country != "CR" || rs[0].Kind != KindHospital {
		t.Errorf("%+v", rs[0])
	}
}

func TestSearchRanking(t *testing.T) {
	ix := buildCR(t)
	first := func(q string) Result {
		t.Helper()
		rs := ix.Search(q, nil, 20)
		if len(rs) == 0 {
			t.Fatalf("%q found nothing", q)
		}
		return rs[0]
	}
	if r := first("san jose"); r.Name != "San José" || r.Kind != KindCity {
		t.Errorf("san jose -> %+v", r)
	}
	if r := first("liberia"); r.Country != "CR" || r.Kind != KindCity {
		t.Errorf("liberia -> %+v", r)
	}
	for _, q := range []string{"limon", "LIMÓN", "Limón", "limón"} {
		found := false
		for _, r := range ix.Search(q, nil, 20) {
			if r.Name == "Limón" || r.Name == "Puerto Limón" {
				found = true
			}
		}
		if !found {
			t.Errorf("%q must find Limón", q)
		}
	}
	// An alternate name matches too.
	if r := first("saint joseph"); r.Name != "San José" {
		t.Errorf("alternate -> %+v", r)
	}
}

func TestMatchScore(t *testing.T) {
	tests := []struct {
		keys, q string
		want    int
	}{
		{"limon", "li", 3},
		{"puerto limon", "limon", 2},
		{"pelimon", "limon", 1},
		{"puerto limon\nlimon", "limon", 3},
		{"pelimon\nrio limon", "limon", 2},
		{"alajuela", "limon", 0},
		{"", "li", 0},
	}
	for _, tc := range tests {
		if got := matchScore(tc.keys, tc.q); got != tc.want {
			t.Errorf("matchScore(%q,%q) = %d, want %d", tc.keys, tc.q, got, tc.want)
		}
	}
}

func TestPrefixBeatsWordBeatsSubstring(t *testing.T) {
	ix := buildCR(t)
	rs := ix.Search("limon", nil, 20)
	pos := map[string]int{}
	for i, r := range rs {
		pos[r.Name] = i
	}
	// "Limón" (province, prefix) and "Limón" alternates of the city rank above "Río Limón" (word) which
	// ranks above "Pelimón" (substring).
	if !(pos["Limón"] < pos["Río Limón"] && pos["Río Limón"] < pos["Pelimón"]) {
		t.Fatalf("order: %v", pos)
	}
}

func TestDistancePenalty(t *testing.T) {
	rows := []string{
		geo(1, "Hospital Central", "Hospital Central", "", 9.0, -84.0, "S", "HSP", "CR", "", 0),
		geo(2, "Hospital Central", "Hospital Central", "", 10.5, -85.0, "S", "HSP", "CR", "", 0),
	}
	ix, err := Build(context.Background(), []string{writeZip(t, t.TempDir(), "CR", rows)})
	if err != nil {
		t.Fatal(err)
	}
	near1 := &Point{Lat: 9.0, Lon: -84.0}
	near2 := &Point{Lat: 10.5, Lon: -85.0}
	if rs := ix.Search("hospital central", near1, 5); rs[0].Lat != 9.0 || rs[0].DistanceKM == nil || *rs[0].DistanceKM > 0.001 {
		t.Errorf("near 1: %+v", rs)
	}
	if rs := ix.Search("hospital central", near2, 5); rs[0].Lat != 10.5 {
		t.Errorf("near 2: %+v", rs)
	}
	if rs := ix.Search("hospital central", nil, 5); rs[0].DistanceKM != nil {
		t.Errorf("no near, no distance: %+v", rs[0])
	}
	// The penalty is distance/100: a 3 point match that is 250 km away loses 2.5 to a 3 point match at 0 km
	// only when the kind weights tie, as here.
	a := ix.Search("hospital central", near1, 5)
	if d := *a[1].DistanceKM; d < 100 {
		t.Errorf("second hospital should be far: %v", d)
	}
}

func TestSearchLimits(t *testing.T) {
	var rows []string
	for i := 0; i < 80; i++ {
		rows = append(rows, geo(i+1, fmt.Sprintf("Villa %02d", i), fmt.Sprintf("Villa %02d", i), "", 9+float64(i)/1000, -84, "P", "PPL", "CR", "", 10))
	}
	ix, err := Build(context.Background(), []string{writeZip(t, t.TempDir(), "CR", rows)})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct{ limit, want int }{{0, 20}, {-3, 20}, {1, 1}, {7, 7}, {50, 50}, {500, 50}}
	for _, tc := range tests {
		if got := len(ix.Search("villa", nil, tc.limit)); got != tc.want {
			t.Errorf("limit %d gave %d, want %d", tc.limit, got, tc.want)
		}
	}
	if got := ix.Search("v", nil, 5); got != nil {
		t.Errorf("a one-letter query finds nothing, got %v", got)
	}
}

func TestNearest(t *testing.T) {
	ix := buildCR(t)
	p := Point{Lat: 9.9325, Lon: -84.0889}
	rs := ix.Nearest(p, KindHospital, 5)
	want := []string{"Hospital San Juan de Dios", "Hospital Calderón Guardia", "Hospital México", "Hospital de Heredia", "Hospital de Alajuela"}
	if len(rs) != len(want) {
		t.Fatalf("got %d results", len(rs))
	}
	prev := -1.0
	for i, r := range rs {
		if r.Name != want[i] {
			t.Errorf("#%d = %s, want %s", i, r.Name, want[i])
		}
		if r.DistanceKM == nil || *r.DistanceKM < prev {
			t.Errorf("distances not ascending at %d", i)
		}
		prev = *r.DistanceKM
		if r.Kind != KindHospital {
			t.Errorf("kind %s", r.Kind)
		}
	}
	if got := ix.Nearest(p, KindClinic, 5); len(got) != 1 || got[0].Name != "Clínica Bíblica" {
		t.Errorf("clinic: %+v", got)
	}
	if got := ix.Nearest(p, KindWater, 5); len(got) != 2 {
		t.Errorf("water: %+v", got)
	}
	if got := ix.Nearest(p, "beach", 5); got != nil {
		t.Errorf("unknown kind: %+v", got)
	}
	if got := ix.Nearest(p, KindHospital, 100); len(got) != 7 {
		t.Errorf("limit clamps to what exists: %d", len(got))
	}
	if got := ix.Nearest(p, KindHospital, 0); len(got) != NearestLimit {
		t.Errorf("default limit: %d", len(got))
	}
	var nilIx *Index
	if nilIx.Nearest(p, KindHospital, 5) != nil || nilIx.Search("abc", nil, 5) != nil || nilIx.Len() != 0 {
		t.Error("a nil index answers nothing")
	}
}

func TestDistanceKM(t *testing.T) {
	// One degree of latitude is about 111.2 km.
	if d := DistanceKM(Point{0, 0}, Point{1, 0}); math.Abs(d-111.19) > 0.1 {
		t.Errorf("%v", d)
	}
	if d := DistanceKM(Point{9, -84}, Point{9, -84}); d != 0 {
		t.Errorf("%v", d)
	}
	// Longitude shrinks with latitude.
	if a, b := DistanceKM(Point{0, 0}, Point{0, 1}), DistanceKM(Point{60, 0}, Point{60, 1}); b > a*0.51 {
		t.Errorf("%v %v", a, b)
	}
}

func TestBuildErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Build(context.Background(), []string{filepath.Join(dir, "missing.zip")}); err == nil {
		t.Error("a missing zip must fail")
	}
	bad := filepath.Join(dir, "bad.zip")
	if err := os.WriteFile(bad, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(context.Background(), []string{bad}); err == nil {
		t.Error("a corrupt zip must fail")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Build(ctx, []string{writeZip(t, dir, "CR", crRows())}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v", err)
	}
}

func TestValidate(t *testing.T) {
	dir := t.TempDir()
	good := writeZip(t, dir, "CR", crRows())
	var eighteen []string
	for _, l := range crRows() {
		eighteen = append(eighteen, strings.Join(strings.Split(l, "\t")[:18], "\t"))
	}
	short := writeZip(t, dir, "BZ", eighteen)
	mixed := crRows()
	for i := 0; i < 3; i++ { // 3 of 23 rows broken is more than 1%
		mixed = append(mixed, "garbage")
	}
	mixedZip := writeZip(t, dir, "PA", mixed)
	empty := writeZip(t, dir, "NI", nil)
	corrupt := filepath.Join(dir, "HN.zip")
	if err := os.WriteFile(corrupt, []byte("xx"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		zips     []string
		previous int
		wantErr  bool
	}{
		{"good", []string{good}, 0, false},
		{"good with a smaller previous", []string{good}, 30, false},
		{"exactly half of the previous", []string{good}, 2 * len(crRows()), false},
		{"18 columns", []string{short}, 0, true},
		{"too many broken rows", []string{mixedZip}, 0, true},
		{"fewer than half of the previous version", []string{good}, 2*len(crRows()) + 1, true},
		{"one good and one bad zip", []string{good, short}, 0, true},
		{"no rows", []string{empty}, 0, true},
		{"corrupt zip", []string{corrupt}, 0, true},
		{"no files", nil, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Validate(context.Background(), tc.zips, tc.previous)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if err != nil {
				var pe *PlausibilityError
				if !errors.As(err, &pe) {
					t.Errorf("want *PlausibilityError, got %T", err)
				}
			}
		})
	}
	// One broken row in a hundred and one is within the 1 % allowance.
	rows := make([]string, 0, 101)
	for i := 0; i < 100; i++ {
		rows = append(rows, crRows()[i%len(crRows())])
	}
	rows = append(rows, "garbage")
	if _, err := Validate(context.Background(), []string{writeZip(t, dir, "GT", rows)}, 0); err != nil {
		t.Errorf("1 in 101 broken rows is fine: %v", err)
	}
}

type copies map[string]ark.Copy

func (c copies) BestCopy(id string) (ark.Copy, error) {
	if x, ok := c[id]; ok {
		return x, nil
	}
	return ark.Copy{}, ark.ErrNotPresent
}

func TestManager(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	zip1 := writeZip(t, dir, "CR", crRows())
	cs := copies{}
	m := &Manager{Copies: cs}
	if m.State() != StateBuilding || m.Index() != nil {
		t.Fatalf("before the first rebuild: %s", m.State())
	}
	m.Rebuild(ctx)
	if m.State() != StateAbsent || m.Index() != nil {
		t.Fatalf("no copy: %s", m.State())
	}

	cs[DefaultItem] = ark.Copy{Dir: dir, Files: []string{zip1}, Manifest: manifest.Manifest{Version: "v1"}}
	builds := 0
	m.BuildFn = func(ctx context.Context, zips []string) (*Index, error) { builds++; return Build(ctx, zips) }
	m.Rebuild(ctx)
	if m.State() != StateReady || m.Index().Len() == 0 || builds != 1 {
		t.Fatalf("ready: %s %d", m.State(), builds)
	}
	if m.Rows() != len(crRows()) || m.Source() != DefaultItem {
		t.Errorf("rows %d source %s", m.Rows(), m.Source())
	}
	m.Rebuild(ctx)
	if builds != 1 {
		t.Errorf("an unchanged copy must not rebuild, builds = %d", builds)
	}

	// A new version swaps the index; a failing build keeps the old one.
	old := m.Index()
	cs[DefaultItem] = ark.Copy{Dir: dir, Files: []string{zip1}, Manifest: manifest.Manifest{Version: "v2"}}
	m.BuildFn = func(context.Context, []string) (*Index, error) { return nil, errors.New("boom") }
	m.Rebuild(ctx)
	if m.Index() != old || m.State() != StateReady {
		t.Errorf("a failed rebuild keeps the old index")
	}
	m.BuildFn = nil
	m.Rebuild(ctx)
	if m.Index() == old || m.State() != StateReady {
		t.Errorf("the new version is swapped in")
	}

	// The drive goes away.
	delete(cs, DefaultItem)
	m.Rebuild(ctx)
	if m.State() != StateAbsent || m.Index() != nil {
		t.Errorf("eject: %s", m.State())
	}

	// A first build that fails is "failed", not "ready".
	cs[DefaultItem] = ark.Copy{Dir: dir, Files: []string{filepath.Join(dir, "missing.zip")}, Manifest: manifest.Manifest{Version: "v3"}}
	m.Rebuild(ctx)
	if m.State() != StateFailed {
		t.Errorf("failed build: %s", m.State())
	}
}

// TestMemoryBudget builds an index of 300 000 synthetic rows (the size of Central America) and checks
// the heap it holds stays under the 60 MB of the spec.
func TestMemoryBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 300 000 row index")
	}
	const n = 300000
	rows := make([]string, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("Villa de San Isidro del Valle %d", i)
		rows = append(rows, geo(i+1, name, name, "Alt Uno,Alt Dos,Alt Tres", 8+float64(i%1000)/100, -85+float64(i%700)/100, "P", "PPL", "CR", fmt.Sprintf("%02d", i%9), i%5000))
	}
	zp := writeZip(t, t.TempDir(), "CR", rows)
	rows = nil

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	ix, err := Build(context.Background(), []string{zp})
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	used := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("%d places use %.1f MB", ix.Len(), float64(used)/(1<<20))
	if used > 60<<20 {
		t.Fatalf("the index holds %d MB, the budget is 60 MB", used>>20)
	}
	runtime.KeepAlive(ix)
}

func TestRegisterValidator(t *testing.T) {
	dir := t.TempDir()
	writeZip(t, dir, "CR", crRows())
	var eighteen []string
	for _, l := range crRows() {
		eighteen = append(eighteen, strings.Join(strings.Split(l, "\t")[:18], "\t"))
	}
	writeZip(t, dir, "PA", eighteen)

	prev := 0
	v := item.NewValidators()
	if err := RegisterValidator(v, func(string) int { return prev }); err != nil {
		t.Fatal(err)
	}
	target := func(id string, files ...string) item.Target {
		tg := item.Target{Meta: item.Meta{ItemID: id, Kind: "maps", SourceType: "url"}, StagedDir: dir}
		for _, f := range files {
			tg.Files = append(tg.Files, item.File{Path: f})
		}
		return tg
	}
	ctx := context.Background()
	if err := v.Run(ctx, target("places-central-america", "CR.zip")); err != nil {
		t.Errorf("good dump: %v", err)
	}
	err := v.Run(ctx, target("places-central-america", "CR.zip", "PA.zip"))
	var ve *item.ValidationError
	if !errors.As(err, &ve) || ve.Code != doctor.SYN020 || ve.Validator != ValidatorName {
		t.Errorf("18 columns must fail with LAI-SYN-020, got %v", err)
	}
	prev = 10 * len(crRows())
	if err := v.Run(ctx, target("places-central-america", "CR.zip")); err == nil {
		t.Error("fewer than half of the previous rows must fail")
	}
	// Other items are not touched.
	if err := v.Run(ctx, target("osm-central-america", "PA.zip")); err != nil {
		t.Errorf("another item: %v", err)
	}
}

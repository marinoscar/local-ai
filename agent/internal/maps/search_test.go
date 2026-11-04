package maps

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/maps/places"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

func geoLine(id int, name, ascii string, lat, lon float64, class, code, admin1 string, pop int) string {
	p := ""
	if pop > 0 {
		p = fmt.Sprint(pop)
	}
	return strings.Join([]string{fmt.Sprint(id), name, ascii, "", fmt.Sprint(lat), fmt.Sprint(lon), class, code, "CR", "", admin1,
		"", "", "", p, "", "", "", ""}, "\t")
}

func placesZip(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "CR.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("CR.txt")
	for _, l := range []string{
		geoLine(1, "San José", "San Jose", 9.93333, -84.08333, "P", "PPLC", "08", 335007),
		geoLine(2, "Provincia de San José", "Provincia de San Jose", 9.8, -84.1, "A", "ADM1", "08", 0),
		geoLine(3, "Limón", "Limon", 9.99074, -83.03596, "P", "PPLA", "07", 94415),
		geoLine(4, "Hospital San Juan de Dios", "Hospital San Juan de Dios", 9.9325, -84.0889, "S", "HSP", "08", 0),
		geoLine(5, "Hospital México", "Hospital Mexico", 9.956, -84.112, "S", "HSP", "08", 0),
	} {
		_, _ = w.Write([]byte(l + "\n"))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

type placesEnv struct {
	*env
	mgr *places.Manager
}

func newPlacesEnv(t *testing.T, state string) *placesEnv {
	t.Helper()
	pe := &placesEnv{}
	pe.env = newEnv(t, func(e *env) {
		if state != "none" {
			e.cp[places.DefaultItem] = ark.Copy{Dir: e.dir, Files: []string{placesZip(t, e.dir)}, Manifest: manifest.Manifest{Version: "1"}}
		}
	})
	if state == "unregistered" {
		return pe
	}
	pe.mgr = &places.Manager{Copies: pe.cp}
	pe.svc.SetPlaces(pe.mgr)
	if state == "ready" || state == "none" {
		pe.mgr.Rebuild(context.Background())
	}
	return pe
}

type searchBody struct {
	Results []struct {
		Name       string   `json:"name"`
		Kind       string   `json:"kind"`
		Lat        float64  `json:"lat"`
		Lon        float64  `json:"lon"`
		Admin1     string   `json:"admin1"`
		Country    string   `json:"country"`
		DistanceKM *float64 `json:"distance_km"`
	} `json:"results"`
	Attribution string `json:"attribution"`
}

func TestSearchRoute(t *testing.T) {
	pe := newPlacesEnv(t, "ready")
	resp, body := pe.do(t, "GET", "/maps/search?q=san%20jose&limit=3", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	var sb searchBody
	if err := json.Unmarshal(body, &sb); err != nil {
		t.Fatal(err)
	}
	if len(sb.Results) == 0 || sb.Results[0].Name != "San José" || sb.Results[0].Kind != "city" || sb.Results[0].Country != "CR" ||
		sb.Results[0].Admin1 != "Provincia de San José" || sb.Results[0].Lat != 9.93333 || sb.Results[0].Lon != -84.08333 {
		t.Fatalf("%s", body)
	}
	if sb.Results[0].DistanceKM != nil {
		t.Error("no near, no distance_km")
	}
	if sb.Attribution != "GeoNames (CC BY 4.0)" {
		t.Errorf("attribution %q", sb.Attribution)
	}

	_, body = pe.do(t, "GET", "/maps/search?q=LIMON&near=9.93,-84.08", nil)
	_ = json.Unmarshal(body, &sb)
	if len(sb.Results) == 0 || sb.Results[0].Name != "Limón" || sb.Results[0].DistanceKM == nil || *sb.Results[0].DistanceKM < 100 {
		t.Fatalf("accent-insensitive with near: %s", body)
	}
	if resp, hb := pe.do(t, "HEAD", "/maps/search?q=limon", nil); resp.StatusCode != 200 || len(hb) != 0 {
		t.Errorf("HEAD %d %d", resp.StatusCode, len(hb))
	}
	if resp, _ := pe.do(t, "POST", "/maps/search?q=limon", nil); resp.StatusCode != 405 {
		t.Errorf("POST %d", resp.StatusCode)
	}
	// An empty result is [] and not null.
	_, body = pe.do(t, "GET", "/maps/search?q=zzzz", nil)
	if !strings.Contains(string(body), `"results":[]`) {
		t.Errorf("%s", body)
	}
}

func TestSearchBadRequests(t *testing.T) {
	pe := newPlacesEnv(t, "ready")
	tests := []struct{ name, path string }{
		{"missing q", "/maps/search"},
		{"q of one character", "/maps/search?q=a"},
		{"q of punctuation", "/maps/search?q=!!"},
		{"bad near", "/maps/search?q=san&near=abc"},
		{"near out of range", "/maps/search?q=san&near=95,0"},
		{"bad limit", "/maps/search?q=san&limit=x"},
		{"zero limit", "/maps/search?q=san&limit=0"},
		{"bad lang", "/maps/search?q=san&lang=fr"},
		{"nearest without lat", "/maps/nearest?lon=-84&kind=hospital"},
		{"nearest lat out of range", "/maps/nearest?lat=91&lon=-84&kind=hospital"},
		{"nearest lon out of range", "/maps/nearest?lat=9&lon=181&kind=hospital"},
		{"nearest unknown kind", "/maps/nearest?lat=9&lon=-84&kind=beach"},
		{"nearest no kind", "/maps/nearest?lat=9&lon=-84"},
		{"nearest NaN", "/maps/nearest?lat=NaN&lon=-84&kind=hospital"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := pe.do(t, "GET", tc.path, nil)
			if resp.StatusCode != 400 || code(body) != doctor.AGT020 {
				t.Fatalf("%d %s", resp.StatusCode, body)
			}
		})
	}
	// A limit above 50 is clamped, not refused.
	if resp, _ := pe.do(t, "GET", "/maps/search?q=san&limit=500", nil); resp.StatusCode != 200 {
		t.Errorf("limit 500: %d", resp.StatusCode)
	}
}

func TestSearchStates(t *testing.T) {
	t.Run("building answers 503", func(t *testing.T) {
		pe := newPlacesEnv(t, "building")
		for _, path := range []string{"/maps/search?q=san", "/maps/nearest?lat=9&lon=-84&kind=hospital"} {
			resp, body := pe.do(t, "GET", path, nil)
			if resp.StatusCode != http.StatusServiceUnavailable || strings.TrimSpace(string(body)) != `{"state":"building"}` {
				t.Fatalf("%s: %d %s", path, resp.StatusCode, body)
			}
		}
		if si := pe.svc.Catalog().Search; si.Available {
			t.Error("search is not available while building")
		}
	})
	t.Run("not installed answers 404 LAI-SVC-032", func(t *testing.T) {
		pe := newPlacesEnv(t, "none")
		resp, body := pe.do(t, "GET", "/maps/search?q=san", nil)
		if resp.StatusCode != 404 || code(body) != doctor.SVC032 {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
		rd := pe.svc.readiness(context.Background())
		if !hasCode(rd.Problems, doctor.SVC032) {
			t.Errorf("readiness %+v", rd)
		}
	})
	t.Run("no manager registered", func(t *testing.T) {
		pe := newPlacesEnv(t, "unregistered")
		resp, body := pe.do(t, "GET", "/maps/search?q=san", nil)
		if resp.StatusCode != 404 || code(body) != doctor.SVC032 {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
		if rd := pe.svc.readiness(context.Background()); hasCode(rd.Problems, doctor.SVC032) {
			t.Errorf("without SVC-11 wired there is no finding: %+v", rd)
		}
	})
	t.Run("the catalog reports the index", func(t *testing.T) {
		pe := newPlacesEnv(t, "ready")
		s := pe.svc.Catalog().Search
		if !s.Available || s.Places != 5 || s.Source == nil || *s.Source != places.DefaultItem {
			t.Fatalf("%+v", s)
		}
		if rd := pe.svc.readiness(context.Background()); hasCode(rd.Problems, doctor.SVC032) {
			t.Errorf("readiness %+v", rd)
		}
	})
}

func TestNearestRoute(t *testing.T) {
	pe := newPlacesEnv(t, "ready")
	resp, body := pe.do(t, "GET", "/maps/nearest?lat=9.9325&lon=-84.0889&kind=hospital&limit=5", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	var sb searchBody
	if err := json.Unmarshal(body, &sb); err != nil {
		t.Fatal(err)
	}
	if len(sb.Results) != 2 || sb.Results[0].Name != "Hospital San Juan de Dios" || sb.Results[1].Name != "Hospital México" {
		t.Fatalf("%s", body)
	}
	if sb.Results[0].DistanceKM == nil || *sb.Results[0].DistanceKM > 0.2 || *sb.Results[1].DistanceKM <= *sb.Results[0].DistanceKM {
		t.Errorf("distances: %s", body)
	}
	if sb.Attribution == "" {
		t.Error("attribution")
	}
}

package places

import (
	"math"
	"sort"
	"strings"
)

// Limits of a query (companion-services.md section 4.4.4).
const (
	MinQueryLen  = 2
	DefaultLimit = 20
	MaxLimit     = 50
	NearestLimit = 5
)

// Point is a place on the earth in degrees.
type Point struct{ Lat, Lon float64 }

// Result is one answer of /maps/search and /maps/nearest.
type Result struct {
	ID         uint32
	Name       string
	Kind       string
	Lat, Lon   float64
	Admin1     string
	Country    string
	DistanceKM *float64
}

const earthKM = 6371.0088

// DistanceKM is the equirectangular distance between two points; it is exact enough at the scale of a
// country and costs one cosine.
func DistanceKM(a, b Point) float64 {
	const rad = math.Pi / 180
	x := (b.Lon - a.Lon) * rad * math.Cos((a.Lat+b.Lat)/2*rad)
	y := (b.Lat - a.Lat) * rad
	return earthKM * math.Hypot(x, y)
}

// matchScore is 3 when the query is a prefix of one of the names, 2 when it is a prefix of a word in
// one, 1 when it is a substring, 0 when it matches none (keys and q are already folded).
func matchScore(keys, q string) int {
	best := 0
	for keys != "" {
		k := keys
		if i := strings.IndexByte(keys, '\n'); i >= 0 {
			k, keys = keys[:i], keys[i+1:]
		} else {
			keys = ""
		}
		switch {
		case strings.HasPrefix(k, q):
			return 3
		case best < 2 && (strings.Contains(k, " "+q)):
			best = 2
		case best < 1 && strings.Contains(k, q):
			best = 1
		}
	}
	return best
}

// kindWeight is the second term of the score: P with a population is log10(pop+1), a province or
// district is 4, hospitals and clinics 3, everything else 1.
func kindWeight(r *row) float64 {
	switch {
	case r.class == 'P' && r.pop > 0:
		return math.Log10(float64(r.pop) + 1)
	case r.class == 'A':
		return 4
	case Kinds[r.kind] == KindHospital || Kinds[r.kind] == KindClinic:
		return 3
	}
	return 1
}

func (ix *Index) result(r *row, near *Point) Result {
	res := Result{ID: r.id, Name: ix.name(r), Kind: Kinds[r.kind], Lat: float64(r.lat), Lon: float64(r.lon),
		Admin1: ix.admin1[r.admin1], Country: string(r.country[:])}
	if near != nil {
		d := DistanceKM(*near, Point{res.Lat, res.Lon})
		res.DistanceKM = &d
	}
	return res
}

type scored struct {
	i     int32
	score float64
	pop   uint32
}

// Search returns the best limit places for q, best first: match*10 + kind weight, minus distance/100
// when near is given. A query shorter than MinQueryLen after folding finds nothing; the caller
// answers 400 for it. limit is clamped to 1..MaxLimit (0 means DefaultLimit).
func (ix *Index) Search(q string, near *Point, limit int) []Result {
	if ix == nil {
		return nil
	}
	q = Fold(q)
	if len(q) < MinQueryLen {
		return nil
	}
	limit = ClampLimit(limit, DefaultLimit)
	top := make([]scored, 0, limit+1)
	better := func(a, b scored) bool {
		if a.score != b.score {
			return a.score > b.score
		}
		if a.pop != b.pop {
			return a.pop > b.pop
		}
		return a.i < b.i
	}
	for i := range ix.rows {
		r := &ix.rows[i]
		m := matchScore(ix.keys(r), q)
		if m == 0 {
			continue
		}
		s := float64(m)*10 + kindWeight(r)
		if near != nil {
			s -= DistanceKM(*near, Point{float64(r.lat), float64(r.lon)}) / 100
		}
		c := scored{i: int32(i), score: s, pop: r.pop}
		if len(top) == limit && !better(c, top[len(top)-1]) {
			continue
		}
		at := sort.Search(len(top), func(j int) bool { return better(c, top[j]) })
		top = append(top, scored{})
		copy(top[at+1:], top[at:])
		top[at] = c
		if len(top) > limit {
			top = top[:limit]
		}
	}
	out := make([]Result, len(top))
	for k, c := range top {
		out[k] = ix.result(&ix.rows[c.i], near)
	}
	return out
}

// ClampLimit returns n limited to 1..MaxLimit, def when n is not positive.
func ClampLimit(n, def int) int {
	switch {
	case n <= 0:
		return def
	case n > MaxLimit:
		return MaxLimit
	}
	return n
}

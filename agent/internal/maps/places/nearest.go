package places

import "sort"

// Nearest returns the limit places of one kind closest to p, nearest first, by a linear scan over the
// rows of that kind (fast enough at this size). An unknown kind finds nothing; the caller answers 400
// for it.
func (ix *Index) Nearest(p Point, kind string, limit int) []Result {
	if ix == nil {
		return nil
	}
	k, ok := kindIndex[kind]
	if !ok {
		return nil
	}
	limit = ClampLimit(limit, NearestLimit)
	type cand struct {
		i int32
		d float64
	}
	top := make([]cand, 0, limit+1)
	for _, i := range ix.byKind[k] {
		r := &ix.rows[i]
		d := DistanceKM(p, Point{float64(r.lat), float64(r.lon)})
		if len(top) == limit && d >= top[len(top)-1].d {
			continue
		}
		at := sort.Search(len(top), func(j int) bool { return d < top[j].d })
		top = append(top, cand{})
		copy(top[at+1:], top[at:])
		top[at] = cand{i, d}
		if len(top) > limit {
			top = top[:limit]
		}
	}
	out := make([]Result, len(top))
	for n, c := range top {
		out[n] = ix.result(&ix.rows[c.i], &p)
	}
	return out
}

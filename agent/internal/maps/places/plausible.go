package places

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/sync/item"
)

// Plausibility thresholds of the GeoNames item (companion-services.md section 4.4.4). GeoNames dumps
// have no pinned hash (trust: tofu), so the content is what gets checked.
const (
	// MinParsedFraction is the share of rows that must parse.
	MinParsedFraction = 0.99
	// MinPreviousFraction is the share of the previous version's rows the new one must have.
	MinPreviousFraction = 0.5
)

// ValidatorName is the name item.Validators shows in reason.json.
const ValidatorName = "geonames_plausible"

// Stats is what Validate counted.
type Stats struct {
	Files  int
	Rows   int
	Parsed int
}

// PlausibilityError is a dump that does not look like GeoNames data. item.Validators turns it into a
// verification failure (the item is quarantined with LAI-SYN-020).
type PlausibilityError struct{ Msg string }

func (e *PlausibilityError) Error() string { return "places: " + e.Msg }

// Validate checks the zip files of a places item: every zip opens and has a data file, at least
// MinParsedFraction of the rows have 19 tab-separated columns and sane numbers, and the row count is
// at least MinPreviousFraction of previousRows (0 means there is no previous version).
func Validate(ctx context.Context, zips []string, previousRows int) (Stats, error) {
	var st Stats
	if len(zips) == 0 {
		return st, &PlausibilityError{Msg: "the item has no zip files"}
	}
	for _, zp := range zips {
		st.Files++
		var rows, parsed int
		err := scanZip(ctx, zp, func(line string) {
			rows++
			if _, ok := ParseRow(line); ok {
				parsed++
			}
		})
		if err != nil {
			if ctx.Err() != nil {
				return st, ctx.Err()
			}
			return st, &PlausibilityError{Msg: err.Error()}
		}
		if rows == 0 {
			return st, &PlausibilityError{Msg: filepath.Base(zp) + " has no rows"}
		}
		if float64(parsed) < MinParsedFraction*float64(rows) {
			return st, &PlausibilityError{Msg: fmt.Sprintf("%s: only %d of %d rows have the 19 GeoNames columns", filepath.Base(zp), parsed, rows)}
		}
		st.Rows += rows
		st.Parsed += parsed
	}
	if previousRows > 0 && float64(st.Rows) < MinPreviousFraction*float64(previousRows) {
		return st, &PlausibilityError{Msg: fmt.Sprintf("%d rows, fewer than half of the %d of the previous version", st.Rows, previousRows)}
	}
	return st, nil
}

// IsPlacesItem reports whether an item id names a place index item (places-central-america, ...).
func IsPlacesItem(id string) bool { return strings.HasPrefix(id, "places-") }

// RegisterValidator registers Validate as the content validator of every places-* item of the item
// machine's "verifying" step (ark-sync.md section 4.5). previous returns the row count of the version
// being replaced (0 for none); it may be nil.
func RegisterValidator(v *item.Validators, previous func(itemID string) int) error {
	return v.Register(ValidatorName,
		func(m item.Meta) bool { return m.Kind == "maps" && IsPlacesItem(m.ItemID) },
		func(ctx context.Context, t item.Target) error {
			var zips []string
			for _, f := range t.Files {
				if strings.HasSuffix(strings.ToLower(f.Path), ".zip") {
					zips = append(zips, filepath.Join(t.StagedDir, filepath.FromSlash(f.Path)))
				}
			}
			prev := 0
			if previous != nil {
				prev = previous(t.ItemID)
			}
			_, err := Validate(ctx, zips, prev)
			return err
		})
}

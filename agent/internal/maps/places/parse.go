package places

import (
	"strconv"
	"strings"
)

// GeoNames columns (0-based) of the per-country dumps; every row has Columns of them.
const (
	Columns = 19

	colID      = 0
	colName    = 1
	colASCII   = 2
	colAlt     = 3
	colLat     = 4
	colLon     = 5
	colClass   = 6
	colCode    = 7
	colCountry = 8
	colAdmin1  = 10
	colPop     = 14
)

// MaxAlternates is how many alternate names a row keeps.
const MaxAlternates = 5

// Row is one parsed GeoNames row with the fields the index keeps.
type Row struct {
	ID         uint32
	Name       string
	ASCII      string
	Alternates []string
	Lat, Lon   float64
	Class      string
	Code       string
	Country    string
	Admin1Code string
	Population uint32
}

// ParseRow reads one tab-separated line. It reports false when the line does not have 19 columns or
// its id, coordinates or population are not numbers (the plausibility check counts those).
func ParseRow(line string) (Row, bool) {
	line = strings.TrimRight(line, "\r\n")
	f := strings.Split(line, "\t")
	if len(f) != Columns {
		return Row{}, false
	}
	id, err := strconv.ParseUint(f[colID], 10, 32)
	if err != nil {
		return Row{}, false
	}
	lat, err1 := strconv.ParseFloat(f[colLat], 64)
	lon, err2 := strconv.ParseFloat(f[colLon], 64)
	if err1 != nil || err2 != nil || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return Row{}, false
	}
	var pop uint64
	if f[colPop] != "" {
		p, err := strconv.ParseUint(f[colPop], 10, 64)
		if err != nil {
			return Row{}, false
		}
		pop = p
		if pop > 1<<32-1 {
			pop = 1<<32 - 1
		}
	}
	r := Row{ID: uint32(id), Name: f[colName], ASCII: f[colASCII], Lat: lat, Lon: lon, Class: f[colClass], Code: f[colCode],
		Country: f[colCountry], Admin1Code: f[colAdmin1], Population: uint32(pop)}
	if f[colAlt] != "" {
		for _, a := range strings.Split(f[colAlt], ",") {
			if a = strings.TrimSpace(a); a != "" && a != r.Name {
				r.Alternates = append(r.Alternates, a)
				if len(r.Alternates) == MaxAlternates {
					break
				}
			}
		}
	}
	return r, true
}

// keepCodes are the feature codes kept besides populated places and ADM1/ADM2 (companion-services.md
// section 4.4.4).
var keepCodes = map[string]bool{
	"HSP": true, "HSPC": true, "HSPD": true, "SCH": true, "CH": true, "PP": true, "AIRP": true, "PRT": true,
	"SPNG": true, "WLL": true, "RSV": true, "LK": true, "STM": true, "MT": true, "VLC": true, "PK": true,
	"PRK": true, "BDG": true, "RDJCT": true,
}

// Keep reports whether the index keeps a row: class P, class A with code ADM1 or ADM2, or one of the
// listed codes.
func Keep(class, code string) bool {
	switch {
	case class == "P":
		return true
	case class == "A":
		return code == "ADM1" || code == "ADM2"
	}
	return keepCodes[code]
}

// Kinds the API reports (the UI translates them).
const (
	KindHospital = "hospital"
	KindClinic   = "clinic"
	KindWater    = "water"
	KindTown     = "town"
	KindCity     = "city"
	KindSchool   = "school"
	KindPolice   = "police"
	KindAirport  = "airport"
	KindMountain = "mountain"
	KindPark     = "park"
	KindPlace    = "place"
)

// Kinds lists every kind, for validation of /maps/nearest.
var Kinds = []string{KindHospital, KindClinic, KindWater, KindTown, KindCity, KindSchool, KindPolice,
	KindAirport, KindMountain, KindPark, KindPlace}

// KindOf is the fixed feature to kind table of companion-services.md section 4.4.4; the first match
// wins.
func KindOf(class, code string, population uint32) string {
	switch code {
	case "HSP":
		return KindHospital
	case "HSPC", "HSPD":
		return KindClinic
	case "SPNG", "WLL", "RSV", "LK", "STM":
		return KindWater
	case "SCH":
		return KindSchool
	case "PP":
		return KindPolice
	case "AIRP":
		return KindAirport
	case "MT", "VLC":
		return KindMountain
	case "PK", "PRK":
		return KindPark
	}
	if class == "P" {
		if code == "PPLC" || code == "PPLA" || population >= 100000 {
			return KindCity
		}
		return KindTown
	}
	return KindPlace
}

// ValidKind reports whether k is one of Kinds.
func ValidKind(k string) bool {
	for _, x := range Kinds {
		if x == k {
			return true
		}
	}
	return false
}

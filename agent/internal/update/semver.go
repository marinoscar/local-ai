package update

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// TagPrefix is the prefix of an agent release tag: node-v<semver> (overview.md section 3.8).
const TagPrefix = "node-v"

// semverRE is a strict semantic version: three numbers without leading zeros and an optional
// pre-release. Build metadata ("+...") is refused: it never appears in a release tag.
var semverRE = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

// maxVersionLen bounds any version text before it is parsed.
const maxVersionLen = 64

// Version is a parsed semantic version.
type Version struct {
	Major, Minor, Patch int
	// Pre is the pre-release part without the dash ("rc.1"), "" for a release.
	Pre string
}

// ParseVersion parses a strict semver such as "1.2.3" or "1.2.3-rc.1". A leading "v", build
// metadata, leading zeros, an empty identifier and anything else is an error. It never looks at a path.
func ParseVersion(s string) (Version, error) {
	if len(s) > maxVersionLen {
		return Version{}, fmt.Errorf("version %q is too long", truncate(s, 16))
	}
	m := semverRE.FindStringSubmatch(s)
	if m == nil {
		return Version{}, fmt.Errorf("%q is not a version such as 1.2.3", truncate(s, 32))
	}
	v := Version{Pre: m[4]}
	v.Major, _ = strconv.Atoi(m[1])
	v.Minor, _ = strconv.Atoi(m[2])
	v.Patch, _ = strconv.Atoi(m[3])
	for _, id := range strings.Split(v.Pre, ".") {
		// A numeric identifier has no leading zeros (semver 2.0.0 item 9).
		if v.Pre != "" && isNumeric(id) && len(id) > 1 && id[0] == '0' {
			return Version{}, fmt.Errorf("%q is not a version such as 1.2.3", truncate(s, 32))
		}
	}
	return v, nil
}

// ParseTag parses a release tag node-v<semver>.
func ParseTag(tag string) (Version, error) {
	rest, ok := strings.CutPrefix(tag, TagPrefix)
	if !ok {
		return Version{}, fmt.Errorf("%q is not a node release tag", truncate(tag, 32))
	}
	return ParseVersion(rest)
}

// String is the canonical text of v.
func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Tag is the release tag of v.
func (v Version) Tag() string { return TagPrefix + v.String() }

// Prerelease reports whether v has a pre-release part.
func (v Version) Prerelease() bool { return v.Pre != "" }

// Compare orders two versions by semver precedence: -1, 0 or +1.
func (v Version) Compare(o Version) int {
	for _, p := range [][2]int{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if p[0] != p[1] {
			if p[0] < p[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case v.Pre == o.Pre:
		return 0
	case v.Pre == "":
		return 1 // a release is above any of its pre-releases
	case o.Pre == "":
		return -1
	}
	a, b := strings.Split(v.Pre, "."), strings.Split(o.Pre, ".")
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := compareIdent(a[i], b[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

func compareIdent(a, b string) int {
	an, bn := isNumeric(a), isNumeric(b)
	switch {
	case an && bn:
		// No leading zeros (checked at parse time), so a longer number is a larger one.
		switch {
		case len(a) < len(b):
			return -1
		case len(a) > len(b):
			return 1
		}
		return strings.Compare(a, b)
	case an:
		return -1 // numeric identifiers sort below alphanumeric ones
	case bn:
		return 1
	}
	return strings.Compare(a, b)
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}

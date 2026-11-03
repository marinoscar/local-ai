package update

import "testing"

func TestParseVersion(t *testing.T) {
	good := map[string]string{
		"0.0.0": "0.0.0", "1.2.3": "1.2.3", "10.20.30": "10.20.30",
		"1.2.3-rc.1": "1.2.3-rc.1", "0.0.0-dev": "0.0.0-dev", "1.0.0-alpha-1": "1.0.0-alpha-1",
	}
	for in, want := range good {
		v, err := ParseVersion(in)
		if err != nil || v.String() != want {
			t.Errorf("%q: got %v, %v", in, v, err)
		}
	}
	for _, in := range []string{
		"", "1", "1.2", "1.2.3.4", "v1.2.3", "01.2.3", "1.02.3", "1.2.03", "1.2.3+build", "1.2.3-", "1.2.3-rc..1",
		"1.2.3-01", "../1.2.3", "1.2.3/../x", "1.2.3\n", " 1.2.3", "1.2.-3", "1.2.3-é", "9999999999.0.0",
		"1.2.3-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	} {
		if _, err := ParseVersion(in); err == nil {
			t.Errorf("%q: want an error", in)
		}
	}
}

func TestParseTag(t *testing.T) {
	v, err := ParseTag("node-v1.4.0")
	if err != nil || v.Tag() != "node-v1.4.0" {
		t.Fatalf("node tag: %v %v", v, err)
	}
	for _, in := range []string{"v1.4.0", "node-1.4.0", "desktop-v1.4.0", "catalog-2026.11.02-1", "node-v", "node-v../1.0.0", "node-vv1.0.0"} {
		if _, err := ParseTag(in); err == nil {
			t.Errorf("%q: want an error", in)
		}
	}
}

func TestCompare(t *testing.T) {
	order := []string{
		"0.0.0-dev", "0.1.0", "0.1.1", "0.2.0-alpha", "0.2.0-alpha.1", "0.2.0-alpha.beta", "0.2.0-beta", "0.2.0-beta.2",
		"0.2.0-beta.11", "0.2.0-rc.1", "0.2.0", "0.10.0", "1.0.0",
	}
	for i, a := range order {
		for j, b := range order {
			va, _ := ParseVersion(a)
			vb, _ := ParseVersion(b)
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := va.Compare(vb); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", a, b, got, want)
			}
		}
	}
}

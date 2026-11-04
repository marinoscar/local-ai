package hardware

import "testing"

func TestParsePhysicalCores(t *testing.T) {
	ht := "processor\t: 0\nphysical id\t: 0\ncore id\t\t: 0\n\nprocessor\t: 1\nphysical id\t: 0\ncore id\t\t: 1\n\n" +
		"processor\t: 2\nphysical id\t: 0\ncore id\t\t: 0\n\nprocessor\t: 3\nphysical id\t: 0\ncore id\t\t: 1\n"
	arm := "processor\t: 0\nBogoMIPS\t: 50\n\nprocessor\t: 1\nBogoMIPS\t: 50\n"
	tests := []struct {
		name    string
		in      string
		logical int
		want    int
	}{
		{"hyper-threads count once", ht, 4, 2},
		{"no core ids (ARM) falls back to logical", arm, 2, 2},
		{"empty falls back to logical", "", 0, 0},
	}
	for _, tc := range tests {
		if got := parsePhysicalCores([]byte(tc.in), tc.logical); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

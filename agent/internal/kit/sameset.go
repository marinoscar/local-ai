package kit

// sameSet reports whether a and b hold the same values, in any order and ignoring repeats. The
// verifier uses it to compare the compute capabilities named by a prebuilt file (-sm75_86) with the
// ones of this computer.
func sameSet(a, b []string) bool {
	in := func(x string, l []string) bool {
		for _, y := range l {
			if x == y {
				return true
			}
		}
		return false
	}
	for _, x := range a {
		if !in(x, b) {
			return false
		}
	}
	for _, y := range b {
		if !in(y, a) {
			return false
		}
	}
	return true
}

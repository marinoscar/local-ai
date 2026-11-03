package resolve

import (
	"reflect"
	"testing"
)

func TestParseKiwixListingPicksEveryDatedFileNewestFirst(t *testing.T) {
	body := readFixture(t, kiwixListingPath)
	got := parseKiwixListing(body, "library_en_all_nopic")
	var versions []string
	for _, e := range got {
		versions = append(versions, e.Version)
		if want := "library_en_all_nopic_" + e.Version + ".zim"; e.File != want {
			t.Errorf("file %q for version %s, want %q", e.File, e.Version, want)
		}
	}
	// 2026-13 is not a month, "experimental" and "Xlibrary" are other books, ".torrent", ".meta4" and
	// ".sha256" are neighbours, the %-escaped href decodes to 2026-02 and the mirrorlist link repeats
	// 2025-10 once.
	want := []string{"2026-03", "2026-02", "2026-01", "2025-10", "2025-09"}
	if !reflect.DeepEqual(versions, want) {
		t.Fatalf("versions %v, want %v", versions, want)
	}
}

func TestParseKiwixListingOtherBooksAndNoMatch(t *testing.T) {
	body := readFixture(t, kiwixListingPath)
	if got := parseKiwixListing(body, "library_en_all_maxi"); len(got) != 1 || got[0].Version != "2026-09" {
		t.Fatalf("maxi: %+v", got)
	}
	if got := parseKiwixListing(body, "library_en_all"); len(got) != 0 {
		t.Fatalf("a prefix of a book name must not match: %+v", got)
	}
	if got := parseKiwixListing(body, "no_such_book"); len(got) != 0 {
		t.Fatalf("unknown book: %+v", got)
	}
	if got := parseKiwixListing([]byte("<html>no links</html>"), "library_en_all_nopic"); len(got) != 0 {
		t.Fatalf("no links: %+v", got)
	}
	// a book name is a literal, never a pattern
	if got := parseKiwixListing(body, "library.*"); len(got) != 0 {
		t.Fatalf("a regex in the book name matched: %+v", got)
	}
}

func TestParseKiwixListingHrefForms(t *testing.T) {
	cases := map[string]string{
		`<a href="b_2026-04.zim">`:                               "b_2026-04.zim",
		`<a href='b_2026-04.zim'>`:                               "b_2026-04.zim",
		`<A HREF=b_2026-04.zim>`:                                 "b_2026-04.zim",
		`<a  href = "b_2026-04.zim" >`:                           "b_2026-04.zim",
		`<a href="./b_2026-04.zim">`:                             "b_2026-04.zim",
		`<a href="/zim/other/b_2026-04.zim">`:                    "b_2026-04.zim",
		`<a href="https://mirror.example.test/z/b_2026-04.zim">`: "b_2026-04.zim",
		`<a href="b_2026-04.zim?x=1#frag">`:                      "b_2026-04.zim",
		`<a href="b%5F2026%2D04.zim">`:                           "b_2026-04.zim",
	}
	for html, want := range cases {
		got := parseKiwixListing([]byte(html), "b")
		if len(got) != 1 || got[0].File != want {
			t.Errorf("%s: %+v, want %s", html, got, want)
		}
	}
	for _, html := range []string{
		`<a href="b_2026-04.zim.torrent">`, `<a href="b_2026-04.zim.meta4">`, `<a href="b_2026-04.zim.sha256">`,
		`<a href="b_2026-00.zim">`, `<a href="b_2026-4.zim">`, `<a href="b_26-04.zim">`, `<a href="dir/">`,
		`<a href="b_2026-04.zim/">`, `<a href="b_2026-04.zim%00">`,
		`<a href="B_2026-04.zim">`,
	} {
		if got := parseKiwixListing([]byte(html), "b"); len(got) != 0 {
			t.Errorf("%s must not match: %+v", html, got)
		}
	}
}

func TestKiwixVersionFile(t *testing.T) {
	if v, ok := kiwixVersionFile("b", "b_2026-04.zim"); !ok || v != "2026-04" {
		t.Fatalf("%q %v", v, ok)
	}
	for _, n := range []string{"b_2026-4.zim", "b_2026-13.zim", "b_2026-04.zimx", "c_2026-04.zim", "b_abcd-04.zim", "b_2026-04"} {
		if _, ok := kiwixVersionFile("b", n); ok {
			t.Errorf("%q must not parse", n)
		}
	}
}

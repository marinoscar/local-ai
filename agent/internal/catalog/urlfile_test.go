package catalog

import "testing"

// ItemPath is the name the catalog gives a file, else the last element of its URL.
func TestURLFileItemPath(t *testing.T) {
	cases := []struct {
		name string
		f    URLFile
		want string
	}{
		{"last element of the URL", URLFile{URL: "https://data.example.test/a/b/tool.tar.gz"}, "tool.tar.gz"},
		{"the query is not part of it", URLFile{URL: "https://data.example.test/a/tool.tar.gz?x=1"}, "tool.tar.gz"},
		{"a trailing slash is dropped", URLFile{URL: "https://data.example.test/a/tool/"}, "tool"},
		{"no path", URLFile{URL: "https://data.example.test/"}, ""},
		{"a written name wins", URLFile{URL: "https://data.example.test/rel/SHA256SUMS", Name: "release/SHA256SUMS"}, "release/SHA256SUMS"},
		{"a URL that does not parse", URLFile{URL: "https://[::1"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.f.ItemPath(); got != tc.want {
				t.Errorf("ItemPath() = %q, want %q", got, tc.want)
			}
		})
	}
}

package kiwix

import (
	"regexp"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

var undatedRe = regexp.MustCompile(`^(.*)_\d{4}-\d{2}$`)

// UndatedName strips a trailing _YYYY-MM from a book name (companion-services.md section 4.1.3):
// kiwix-serve also serves each book under that alias, so links built from it survive ZIM updates. A
// name without a date is returned as it is.
func UndatedName(name string) string {
	if m := undatedRe.FindStringSubmatch(strings.TrimSpace(name)); m != nil {
		return m[1]
	}
	return name
}

// BookMap is the books[] of the kiwix row of GET /services: content id, undated book name and the
// root the chosen copy is on, in the order of the library.
func BookMap(books []BookRef) []health.Book {
	out := make([]health.Book, 0, len(books))
	for _, b := range books {
		out = append(out, health.Book{ContentID: b.ContentID, Name: UndatedName(b.Name), Root: b.Root})
	}
	return out
}

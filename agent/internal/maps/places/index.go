package places

import (
	"archive/zip"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"strings"
)

// Index is the in-memory place index. It is immutable once Build returns, so queries need no lock.
// The compact form (float32 coordinates, one string arena, interned admin1 names) keeps about 300 000
// rows well inside the 60 MB budget of the spec.
type Index struct {
	rows   []row
	blob   string   // names and search keys, back to back
	admin1 []string // interned admin1 names, by row.admin1
	byKind [kindCount][]int32
	// total is the number of data rows read from the dumps (kept or not), what the plausibility check
	// of the next version is compared with.
	total int
}

type row struct {
	id       uint32
	lat, lon float32
	pop      uint32
	nameOff  uint32
	keysOff  uint32
	nameLen  uint16
	keysLen  uint16
	admin1   uint16
	kind     uint8
	class    byte
	country  [2]byte
}

const kindCount = 11

var kindIndex = func() map[string]uint8 {
	m := map[string]uint8{}
	for i, k := range Kinds {
		m[k] = uint8(i)
	}
	return m
}()

// Len is the number of places in the index.
func (ix *Index) Len() int {
	if ix == nil {
		return 0
	}
	return len(ix.rows)
}

// TotalRows is the number of data rows the dumps had when the index was built.
func (ix *Index) TotalRows() int {
	if ix == nil {
		return 0
	}
	return ix.total
}

func (ix *Index) name(r *row) string { return ix.blob[r.nameOff : r.nameOff+uint32(r.nameLen)] }
func (ix *Index) keys(r *row) string { return ix.blob[r.keysOff : r.keysOff+uint32(r.keysLen)] }

// Build reads the GeoNames zip files (each holds <CC>.txt) and returns the index. Nothing is extracted
// to disk. The context is checked between rows. A file that cannot be read fails the whole build, so
// the previous index stays in use.
func Build(ctx context.Context, zips []string) (*Index, error) {
	var (
		blob     strings.Builder
		rows     []row
		a1Table  = map[string]uint16{} // "CC.code" -> index in a1Names
		a1Names  []string
		a1ByName = map[string]string{} // "CC.code" -> name, from the ADM1 rows
		total    int
	)
	intern := func(key string) uint16 {
		if i, ok := a1Table[key]; ok {
			return i
		}
		if len(a1Names) >= math.MaxUint16 {
			return 0
		}
		a1Names = append(a1Names, key)
		a1Table[key] = uint16(len(a1Names) - 1)
		return uint16(len(a1Names) - 1)
	}
	intern("") // index 0 means no admin1
	for _, zp := range zips {
		err := scanZip(ctx, zp, func(line string) {
			total++
			r, ok := ParseRow(line)
			if !ok {
				return
			}
			if r.Class == "A" && r.Code == "ADM1" {
				a1ByName[r.Country+"."+r.Admin1Code] = r.Name
			}
			if !Keep(r.Class, r.Code) || len(r.Country) != 2 {
				return
			}
			kind := KindOf(r.Class, r.Code, r.Population)
			nameOff := blob.Len()
			blob.WriteString(truncate(r.Name, math.MaxUint16))
			nameLen := blob.Len() - nameOff
			keysOff := blob.Len()
			writeKeys(&blob, r)
			var a1 uint16
			if r.Admin1Code != "" {
				a1 = intern(r.Country + "." + r.Admin1Code)
			}
			rows = append(rows, row{
				id: r.ID, lat: float32(r.Lat), lon: float32(r.Lon), pop: r.Population,
				nameOff: uint32(nameOff), nameLen: uint16(nameLen), keysOff: uint32(keysOff), keysLen: uint16(blob.Len() - keysOff),
				admin1: a1, kind: kindIndex[kind], class: r.Class[0], country: [2]byte{r.Country[0], r.Country[1]},
			})
		})
		if err != nil {
			return nil, err
		}
	}
	// Resolve the admin1 names now that every dump's ADM1 rows are known.
	for i := 1; i < len(a1Names); i++ {
		if n, ok := a1ByName[a1Names[i]]; ok {
			a1Names[i] = n
		} else {
			a1Names[i] = ""
		}
	}
	ix := &Index{rows: rows, blob: blob.String(), admin1: a1Names, total: total}
	for i := range ix.rows {
		k := ix.rows[i].kind
		ix.byKind[k] = append(ix.byKind[k], int32(i))
	}
	return ix, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// writeKeys writes the folded, de-duplicated names of a row separated by '\n'.
func writeKeys(b *strings.Builder, r Row) {
	seen := make([]string, 0, 2+len(r.Alternates))
	add := func(s string) {
		f := Fold(s)
		if f == "" {
			return
		}
		for _, x := range seen {
			if x == f {
				return
			}
		}
		if b.Len() > 0 && len(seen) > 0 {
			b.WriteByte('\n')
		}
		seen = append(seen, f)
		b.WriteString(f)
	}
	add(r.Name)
	add(r.ASCII)
	for _, a := range r.Alternates {
		add(a)
	}
}

// maxLine: a line longer than this is not a GeoNames row and is skipped.
const maxLine = 1 << 20

// dumpFile picks the data file of a GeoNames zip: the .txt that is not the readme.
func dumpFile(zr *zip.ReadCloser) *zip.File {
	for _, f := range zr.File {
		n := strings.ToLower(path.Base(f.Name))
		if strings.HasSuffix(n, ".txt") && !strings.HasPrefix(n, "readme") && !f.FileInfo().IsDir() {
			return f
		}
	}
	return nil
}

// scanZip calls fn for every line of the data file of the zip at p.
func scanZip(ctx context.Context, p string, fn func(line string)) error {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return fmt.Errorf("places: %s: %w", path.Base(p), err)
	}
	defer zr.Close()
	f := dumpFile(zr)
	if f == nil {
		return fmt.Errorf("places: %s has no data file", path.Base(p))
	}
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("places: %s: %w", path.Base(p), err)
	}
	defer rc.Close()
	br := bufio.NewReaderSize(rc, 64<<10)
	for n := 0; ; n++ {
		if n%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		line, err := br.ReadString('\n')
		if len(line) > maxLine {
			line = ""
		}
		if t := strings.TrimRight(line, "\r\n"); t != "" {
			fn(t)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("places: %s: %w", path.Base(p), err)
		}
	}
}

package places

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// geo builds one 19-column GeoNames line.
func geo(id int, name, ascii, alt string, lat, lon float64, class, code, cc, admin1 string, pop int) string {
	p := ""
	if pop > 0 {
		p = fmt.Sprint(pop)
	}
	return strings.Join([]string{fmt.Sprint(id), name, ascii, alt, fmt.Sprint(lat), fmt.Sprint(lon), class, code, cc, "", admin1,
		"", "", "", p, "", "", "America/Costa_Rica", "2020-01-01"}, "\t")
}

// crRows is a small Costa Rica sample: provinces, towns, the capital and a ring of hospitals.
func crRows() []string {
	return []string{
		geo(3621849, "San José", "San Jose", "San Jose,Saint Joseph,Сан-Хосе", 9.93333, -84.08333, "P", "PPLC", "CR", "08", 335007),
		geo(3621850, "Provincia de San José", "Provincia de San Jose", "San Jose", 9.83333, -84.08333, "A", "ADM1", "CR", "08", 1404242),
		geo(3623279, "Guanacaste", "Guanacaste", "", 10.5, -85.25, "A", "ADM1", "CR", "05", 354154),
		geo(3623280, "Limón", "Limon", "Puerto Limon,Limon Province", 10.0, -83.25, "A", "ADM1", "CR", "07", 386862),
		geo(3623281, "Liberia", "Liberia", "", 10.63504, -85.43772, "P", "PPLA", "CR", "05", 49000),
		geo(3623282, "Puerto Limón", "Puerto Limon", "Limon", 9.99074, -83.03596, "P", "PPLA", "CR", "07", 94415),
		geo(3623283, "San José de la Montaña", "San Jose de la Montana", "", 10.06667, -84.1, "P", "PPL", "CR", "02", 1500),
		geo(3623284, "Pelimón", "Pelimon", "", 10.2, -84.3, "P", "PPL", "CR", "02", 300),
		geo(3623285, "Río Limón", "Rio Limon", "", 10.3, -84.4, "H", "STM", "CR", "02", 0),
		geo(3623286, "Liberia", "Liberia", "", 15.1, -88.2, "P", "PPL", "HN", "03", 120),
		// Hospitals around 9.93, -84.08, closest first.
		geo(100001, "Hospital San Juan de Dios", "Hospital San Juan de Dios", "", 9.9325, -84.0889, "S", "HSP", "CR", "08", 0),
		geo(100002, "Hospital Calderón Guardia", "Hospital Calderon Guardia", "", 9.9402, -84.0710, "S", "HSP", "CR", "08", 0),
		geo(100003, "Hospital México", "Hospital Mexico", "", 9.9560, -84.1120, "S", "HSP", "CR", "08", 0),
		geo(100004, "Hospital de Heredia", "Hospital de Heredia", "", 9.9980, -84.1170, "S", "HSP", "CR", "04", 0),
		geo(100005, "Hospital de Cartago", "Hospital de Cartago", "", 9.8640, -83.9190, "S", "HSP", "CR", "03", 0),
		geo(100006, "Hospital de Alajuela", "Hospital de Alajuela", "", 10.0160, -84.2140, "S", "HSP", "CR", "02", 0),
		geo(100007, "Hospital de Liberia", "Hospital de Liberia", "", 10.6310, -85.4400, "S", "HSP", "CR", "05", 0),
		geo(100008, "Clínica Bíblica", "Clinica Biblica", "", 9.9340, -84.0800, "S", "HSPC", "CR", "08", 0),
		geo(100009, "Naciente El Rodeo", "Naciente El Rodeo", "", 9.9300, -84.0600, "H", "SPNG", "CR", "08", 0),
		geo(100010, "Escuela Buenaventura", "Escuela Buenaventura", "", 9.9310, -84.0850, "S", "SCH", "CR", "08", 0),
		geo(100011, "Cerrito sin interés", "Cerrito sin interes", "", 9.9, -84.0, "T", "HLL", "CR", "08", 0), // not kept
		geo(100012, "Iglesia Central", "Iglesia Central", "", 9.9335, -84.0840, "S", "CH", "CR", "08", 0),
	}
}

// writeZip writes <dir>/<name>.zip holding <name>.txt with the lines and a readme.txt.
func writeZip(t testing.TB, dir, name string, lines []string) string {
	t.Helper()
	p := filepath.Join(dir, name+".zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("readme.txt")
	_, _ = w.Write([]byte("GeoNames readme"))
	w, _ = zw.Create(name + ".txt")
	for _, l := range lines {
		_, _ = w.Write([]byte(l + "\n"))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func buildCR(t testing.TB) *Index {
	t.Helper()
	ix, err := Build(context.Background(), []string{writeZip(t, t.TempDir(), "CR", crRows())})
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

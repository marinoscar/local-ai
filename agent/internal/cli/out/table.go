package out

import "strings"

// Col is one table column: its header (plain words) and whether its cells are right-aligned
// (sizes, counts).
type Col struct {
	Title string
	Right bool
}

// Pair is one line of a key/value block.
type Pair struct{ Label, Value string }

const (
	// colGap is the space between table columns ("two spaces between columns").
	colGap = "  "
	// minColWidth is the narrowest a column is squeezed to fit the line.
	minColWidth = 6
)

// Table prints cols and rows to stdout: the header, then one line per row, two spaces between
// columns, no box drawing. Short rows are padded with empty cells. When the table is wider than the
// line, the widest column other than the first is shortened with an ellipsis until it fits (down to
// a floor), so lines stay within the width. Cells may carry colour escapes (Dim and friends); their
// width is measured without them, but a cell with colour is never cut.
func (p *Printer) Table(cols []Col, rows [][]string) {
	if p.o.JSON || len(cols) == 0 {
		return
	}
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = DisplayWidth(c.Title)
	}
	for _, r := range rows {
		for i := range cols {
			if i < len(r) {
				if w := DisplayWidth(r[i]); w > widths[i] {
					widths[i] = w
				}
			}
		}
	}
	p.fitColumns(widths)
	p.writeRow(widths, cols, titles(cols))
	for _, r := range rows {
		p.writeRow(widths, cols, r)
	}
}

func titles(cols []Col) []string {
	t := make([]string, len(cols))
	for i, c := range cols {
		t[i] = c.Title
	}
	return t
}

// fitColumns shrinks widths until the row fits the line, taking from the widest column other than
// the first (the first is the row's name; a lone column may be shrunk too).
func (p *Printer) fitColumns(widths []int) {
	gaps := (len(widths) - 1) * len(colGap)
	for {
		total := gaps
		for _, w := range widths {
			total += w
		}
		if total <= p.o.Width {
			return
		}
		pick := -1
		for i, w := range widths {
			if (i > 0 || len(widths) == 1) && w > minColWidth && (pick < 0 || w > widths[pick]) {
				pick = i
			}
		}
		if pick < 0 {
			return // nothing left to squeeze: keep the overflow rather than cut names
		}
		over := total - p.o.Width
		cut := widths[pick] - minColWidth
		if over < cut {
			cut = over
		}
		widths[pick] -= cut
	}
}

func (p *Printer) writeRow(widths []int, cols []Col, cells []string) {
	var b strings.Builder
	for i := range cols {
		cell := ""
		if i < len(cells) {
			cell = Truncate(cells[i], widths[i], p.sym.Ellipsis)
		}
		pad := widths[i] - DisplayWidth(cell)
		if pad < 0 {
			pad = 0
		}
		if i > 0 {
			b.WriteString(colGap)
		}
		switch {
		case cols[i].Right:
			b.WriteString(strings.Repeat(" ", pad) + cell)
		case i == len(cols)-1:
			b.WriteString(cell) // no trailing spaces on the last column
		default:
			b.WriteString(cell + strings.Repeat(" ", pad))
		}
	}
	p.stdoutLine(strings.TrimRight(b.String(), " "))
}

// KV prints a label/value block as `aictl status` shows it: labels padded to the longest, two
// spaces, then the value, wrapped under itself when it is longer than the line (a value that fits is
// printed as written). Labels are plain.
func (p *Printer) KV(pairs []Pair) {
	if p.o.JSON || len(pairs) == 0 {
		return
	}
	lw := 0
	for _, kv := range pairs {
		if w := DisplayWidth(kv.Label); w > lw {
			lw = w
		}
	}
	indent := strings.Repeat(" ", lw+len(colGap))
	for _, kv := range pairs {
		first := kv.Label + strings.Repeat(" ", lw-DisplayWidth(kv.Label)) + colGap
		if DisplayWidth(first)+DisplayWidth(kv.Value) <= p.o.Width {
			// It fits: keep the value as written (aligned columns inside a value survive).
			p.stdoutLine(strings.TrimRight(first+kv.Value, " "))
			continue
		}
		for _, l := range Wrap(kv.Value, p.o.Width, first, indent) {
			p.stdoutLine(l)
		}
	}
}

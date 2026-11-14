// Package theme is the terminal face of Corona (ADR-0014): the colours aictl's output, progress
// lines and menu use, at the depth the terminal supports. Colour never carries meaning alone (the
// symbols of aictl.md section 3.3 are always printed), and the decision whether to colour at all
// stays out.ColorEnabled (TTY, --no-color, NO_COLOR, TERM=dumb, --json).
package theme

import "strings"

// Depth is how many colours the terminal can show.
type Depth int

// Depths. Basic is the 8 ANSI colours every terminal has (the codes aictl always used), so a
// terminal that announces nothing looks exactly as before.
const (
	Basic Depth = iota
	Ansi256
	TrueColor
)

// DetectDepth reads COLORTERM (truecolor or 24bit: full colour) and TERM (*256color*: 256 colours).
func DetectDepth(getenv func(string) string) Depth {
	if getenv == nil {
		return Basic
	}
	switch strings.ToLower(getenv("COLORTERM")) {
	case "truecolor", "24bit":
		return TrueColor
	}
	if strings.Contains(getenv("TERM"), "256color") {
		return Ansi256
	}
	return Basic
}

// Role is what a piece of text is, not which colour it gets.
type Role int

// Roles.
const (
	OK       Role = iota // ✓, success: Corona green
	Info                 // i: Corona info blue
	Warn                 // !: Corona amber
	Error                // ✗: Corona red
	Critical             // a critical ✗: red, bold
	Progress             // the spinner and … of work in progress: Corona gold
	Dim                  // ids, paths, times
	Accent               // the mark and the product name: Corona gold, bold
	Selected             // the menu line under the cursor: dark text on gold (reverse video in Basic)
)

// The Corona colours of packages/ui/brand/tokens.json, as RGB.
var rgb = map[Role][3]int{
	OK:       {74, 222, 128},  // ok #4ADE80
	Info:     {125, 182, 255}, // info #7DB6FF
	Warn:     {245, 165, 36},  // warn #F5A524
	Error:    {248, 113, 113}, // bad #F87171
	Critical: {248, 113, 113},
	Progress: {242, 181, 68},  // gold #F2B544
	Dim:      {149, 143, 163}, // textDim #958FA3
	Accent:   {242, 181, 68},
}

// The nearest xterm-256 colours.
var c256 = map[Role]int{OK: 78, Info: 111, Warn: 214, Error: 210, Critical: 210, Progress: 215, Dim: 246, Accent: 215}

// The 8-colour codes aictl used before Corona (aictl.md section 3.3).
var basic = map[Role]string{OK: "32", Info: "34", Warn: "33", Error: "31", Critical: "1;31", Progress: "36", Dim: "2", Accent: "1;33", Selected: "7"}

// SGR is the select-graphic-rendition parameter string of role at depth d (the part between "\x1b["
// and "m").
func SGR(r Role, d Depth) string {
	bold := ""
	if r == Critical || r == Accent {
		bold = "1;"
	}
	switch d {
	case TrueColor:
		if r == Selected {
			return "1;38;2;26;18;6;48;2;242;181;68" // onGold #1A1206 on gold #F2B544
		}
		c := rgb[r]
		return bold + "38;2;" + itoa(c[0]) + ";" + itoa(c[1]) + ";" + itoa(c[2])
	case Ansi256:
		if r == Selected {
			return "1;38;5;234;48;5;215"
		}
		return bold + "38;5;" + itoa(c256[r])
	}
	return basic[r]
}

// Paint wraps s in role's colour at depth d; s unchanged when on is false or s is empty.
func Paint(on bool, d Depth, r Role, s string) string {
	if !on || s == "" {
		return s
	}
	return "\x1b[" + SGR(r, d) + "m" + s + "\x1b[0m"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [4]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

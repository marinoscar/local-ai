// Package media holds the test files the drill embeds (drill.md section 3.2): nothing is fetched at
// run time, so the drill works with the network cable pulled. DRILL-03 ships seven.png; the speech
// samples en.wav and es.wav are DRILL-04's.
package media

import _ "embed"

//go:embed seven.png
var seven []byte

// SevenPNG returns seven.png: 512x512, a white background, a large black digit 7 and a red circle in
// the top-left corner. Drill check 6 sends it to each vision model and asks which digit is written.
// The slice is a copy; the caller may keep or change it.
func SevenPNG() []byte { return append([]byte(nil), seven...) }

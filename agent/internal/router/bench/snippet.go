package bench

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MarshalJSONFile is the content of the result file: the JSON of llama-router.md section 4.13 step
// 6 on one line, newline-terminated.
func (r *Result) MarshalJSONFile() ([]byte, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ShortGPUName shortens a GPU name for the catalog comment: the vendor and brand words are dropped
// and "with Max-Q Design" becomes "Max-Q", then the memory size follows in GB, so "NVIDIA GeForce
// RTX 2070 with Max-Q Design" with 7982 MiB reads "RTX 2070 Max-Q 8 GB". totalMiB <= 0 leaves the
// size out. Characters that would break a YAML comment (a newline) are removed.
func ShortGPUName(name string, totalMiB int64) string {
	n := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, name)
	n = strings.ReplaceAll(n, " with Max-Q Design", " Max-Q")
	for _, w := range []string{"NVIDIA ", "GeForce "} {
		n = strings.ReplaceAll(n, w, "")
	}
	n = strings.Join(strings.Fields(n), " ")
	if totalMiB > 0 {
		n += fmt.Sprintf(" %d GB", (totalMiB+512)/1024)
	}
	return n
}

// Snippet is the YAML for the catalog PR (llama-router.md section 4.13 step 6, aictl.md section
// 4.24), without indentation:
//
//	requirements:
//	  min_vram_mib: 7240   # measured on RTX 2070 Max-Q 8 GB, build f3586e66b-cuda, 2026-11-02
//
// A CPU-only result suggests min_ram_mib and the comment says "CPU only". It returns "" when the
// run was disturbed or measured nothing: no suggestion is printed then.
func (r *Result) Snippet() string {
	key, value, ok := r.SuggestedMiB()
	if !ok {
		return ""
	}
	where := "CPU only"
	if r.GPU != nil {
		var total int64
		if r.GPUTotalMiB != nil {
			total = *r.GPUTotalMiB
		}
		where = ShortGPUName(*r.GPU, total)
	}
	date := r.MeasuredAt
	if len(date) >= 10 {
		date = date[:10]
	}
	return fmt.Sprintf("requirements:\n  %s: %d   # measured on %s, build %s, %s\n", key, value, where, r.Build, date)
}

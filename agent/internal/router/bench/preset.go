package bench

import (
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
)

// Preset is what the engine reads from a model's INI section (llama-router.md section 3.5.1): the
// weights file, the context the router pins, the GPU layers and flash attention. The bench runs the
// model the way the router does, so its numbers apply to the preset as generated.
type Preset struct {
	// Weights is `model`, an absolute path (the first shard of a multi-shard model).
	Weights string
	// Ctx is `c`, the pinned context in tokens.
	Ctx int64
	// NGL is `n-gpu-layers` ("all" or a number); "" when the section does not say.
	NGL string
	// FlashAttn is `flash-attn` (on, off or auto); "" when the section does not say.
	FlashAttn string
}

// ParsePreset reads an INI section ("[id]" header optional, "key = value" lines, ";" and "#"
// comments). A key that appears twice keeps its last value. The weights path must be absolute and
// the context a positive number, else the model cannot be benchmarked as the router runs it.
func ParsePreset(text string) (Preset, error) {
	var p Preset
	var ctxText string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == ';' || line[0] == '#' || line[0] == '[' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "model":
			p.Weights = v
		case "c", "ctx-size":
			ctxText = v
		case "n-gpu-layers":
			p.NGL = v
		case "flash-attn":
			p.FlashAttn = v
		}
	}
	if p.Weights == "" {
		return p, errors.New("the model preset has no model file")
	}
	if !path.IsAbs(p.Weights) {
		return p, fmt.Errorf("the model file %q is not an absolute path", p.Weights)
	}
	n, err := strconv.ParseInt(ctxText, 10, 64)
	if err != nil || n <= 0 {
		return p, fmt.Errorf("the model preset has no usable context (c = %q)", ctxText)
	}
	p.Ctx = n
	return p, nil
}

// allLayers is what "all" becomes for llama-bench, which takes a number (the unit uses the router's
// own "all"; spec example 3.5.2 uses 99 for a model that fits entirely).
const allLayers = "99"

// BenchArgs is the argument list of llama-bench for this preset (llama-router.md section 4.13
// step 4): `-m <weights> -ngl <n> -fa <0|1> -p 512 -n 128 -r 3 -o json`. llama-bench takes
// flash attention as 0 or 1: on is 1, off is 0, and auto (or absent) leaves its default. A preset
// without n-gpu-layers leaves -ngl out.
func (p Preset) BenchArgs() []string {
	args := []string{"-m", p.Weights}
	switch ngl := strings.ToLower(p.NGL); ngl {
	case "":
	case "all":
		args = append(args, "-ngl", allLayers)
	default:
		args = append(args, "-ngl", ngl)
	}
	switch strings.ToLower(p.FlashAttn) {
	case "on", "true", "1":
		args = append(args, "-fa", "1")
	case "off", "false", "0":
		args = append(args, "-fa", "0")
	}
	return append(args, "-p", "512", "-n", "128", "-r", "3", "-o", "json")
}

package build

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"os"
	"testing"
)

// This file writes a tiny but valid llama GGUF at run time, for the real llama build test: the repo
// carries no model (and must not commit a binary), and the smoke test of a build (rebuild-kit.md
// section 4.3.3) needs something llama-cli can load and write a few words with. The model has one layer,
// 64 embedding dimensions, a byte-level SentencePiece vocabulary and random F32 weights from a fixed
// seed: it writes nonsense, but it exercises the tokenizer, the graph and the sampler of the build.

// ggufType is the value type of a metadata entry.
type ggufType uint32

const (
	ggufUint32  ggufType = 4
	ggufInt32   ggufType = 5
	ggufFloat32 ggufType = 6
	ggufBool    ggufType = 7
	ggufString  ggufType = 8
	ggufArray   ggufType = 9
)

type ggufWriter struct{ bytes.Buffer }

func (w *ggufWriter) u32(v uint32) { _ = binary.Write(w, binary.LittleEndian, v) }
func (w *ggufWriter) u64(v uint64) { _ = binary.Write(w, binary.LittleEndian, v) }
func (w *ggufWriter) str(s string) { w.u64(uint64(len(s))); w.WriteString(s) }

// kv writes one metadata entry; val is a uint32, int32, float32, bool, string, or a slice of
// string, float32 or int32 (an array).
func (w *ggufWriter) kv(key string, val any) {
	w.str(key)
	switch v := val.(type) {
	case uint32:
		w.u32(uint32(ggufUint32))
		w.u32(v)
	case float32:
		w.u32(uint32(ggufFloat32))
		_ = binary.Write(w, binary.LittleEndian, v)
	case bool:
		w.u32(uint32(ggufBool))
		b := byte(0)
		if v {
			b = 1
		}
		w.WriteByte(b)
	case string:
		w.u32(uint32(ggufString))
		w.str(v)
	case []string:
		w.u32(uint32(ggufArray))
		w.u32(uint32(ggufString))
		w.u64(uint64(len(v)))
		for _, s := range v {
			w.str(s)
		}
	case []float32:
		w.u32(uint32(ggufArray))
		w.u32(uint32(ggufFloat32))
		w.u64(uint64(len(v)))
		for _, f := range v {
			_ = binary.Write(w, binary.LittleEndian, f)
		}
	case []int32:
		w.u32(uint32(ggufArray))
		w.u32(uint32(ggufInt32))
		w.u64(uint64(len(v)))
		for _, n := range v {
			_ = binary.Write(w, binary.LittleEndian, n)
		}
	default:
		panic(fmt.Sprintf("gguf: unsupported value %T", val))
	}
}

type ggufTensor struct {
	name string
	dims []uint64 // ne[0] first (the fastest-varying)
	data []float32
}

// tinyGGUF returns the bytes of the tiny llama model.
func tinyGGUF() []byte {
	const (
		nEmbd  = 64
		nHead  = 4
		nFF    = 128
		nLayer = 1
		align  = 32
	)
	// SentencePiece vocabulary: unknown, bos, eos, the 256 byte tokens, then a few pieces.
	tokens := []string{"<unk>", "<s>", "</s>"}
	types := []int32{2, 3, 3}
	scores := []float32{0, 0, 0}
	for b := 0; b < 256; b++ {
		tokens = append(tokens, fmt.Sprintf("<0x%02X>", b))
		types = append(types, 6)
		scores = append(scores, 0)
	}
	for i, p := range []string{"▁", "▁Hello", "▁world", "He", "llo", "H", "e", "l", "o", "w", "r", "d"} {
		tokens = append(tokens, p)
		types = append(types, 1)
		scores = append(scores, float32(-1-i))
	}
	nVocab := len(tokens)

	rng := rand.New(rand.NewSource(7))
	weights := func(n int) []float32 {
		out := make([]float32, n)
		for i := range out {
			out[i] = float32(rng.NormFloat64()) * 0.05
		}
		return out
	}
	ones := func(n int) []float32 {
		out := make([]float32, n)
		for i := range out {
			out[i] = 1
		}
		return out
	}
	mat := func(name string, ne0, ne1 int) ggufTensor {
		return ggufTensor{name: name, dims: []uint64{uint64(ne0), uint64(ne1)}, data: weights(ne0 * ne1)}
	}
	vec := func(name string, n int) ggufTensor {
		return ggufTensor{name: name, dims: []uint64{uint64(n)}, data: ones(n)}
	}
	tensors := []ggufTensor{mat("token_embd.weight", nEmbd, nVocab), vec("output_norm.weight", nEmbd), mat("output.weight", nEmbd, nVocab)}
	for l := 0; l < nLayer; l++ {
		p := fmt.Sprintf("blk.%d.", l)
		tensors = append(tensors,
			vec(p+"attn_norm.weight", nEmbd),
			mat(p+"attn_q.weight", nEmbd, nEmbd), mat(p+"attn_k.weight", nEmbd, nEmbd),
			mat(p+"attn_v.weight", nEmbd, nEmbd), mat(p+"attn_output.weight", nEmbd, nEmbd),
			vec(p+"ffn_norm.weight", nEmbd),
			mat(p+"ffn_gate.weight", nEmbd, nFF), mat(p+"ffn_down.weight", nFF, nEmbd), mat(p+"ffn_up.weight", nEmbd, nFF),
		)
	}

	var meta ggufWriter
	entries := 0
	kv := func(k string, v any) { meta.kv(k, v); entries++ }
	kv("general.architecture", "llama")
	kv("general.name", "tiny-test")
	kv("llama.context_length", uint32(128))
	kv("llama.embedding_length", uint32(nEmbd))
	kv("llama.block_count", uint32(nLayer))
	kv("llama.feed_forward_length", uint32(nFF))
	kv("llama.attention.head_count", uint32(nHead))
	kv("llama.attention.head_count_kv", uint32(nHead))
	kv("llama.attention.layer_norm_rms_epsilon", float32(1e-5))
	kv("llama.rope.dimension_count", uint32(nEmbd/nHead))
	kv("llama.vocab_size", uint32(nVocab))
	kv("tokenizer.ggml.model", "llama")
	kv("tokenizer.ggml.tokens", tokens)
	kv("tokenizer.ggml.scores", scores)
	kv("tokenizer.ggml.token_type", types)
	kv("tokenizer.ggml.bos_token_id", uint32(1))
	kv("tokenizer.ggml.eos_token_id", uint32(2))
	kv("tokenizer.ggml.unknown_token_id", uint32(0))
	kv("tokenizer.ggml.add_bos_token", true)

	// Tensor infos, then the data section aligned to 32 bytes, each tensor at an aligned offset.
	var infos ggufWriter
	var data bytes.Buffer
	pad := func(b *bytes.Buffer) {
		for b.Len()%align != 0 {
			b.WriteByte(0)
		}
	}
	for _, t := range tensors {
		pad(&data)
		infos.str(t.name)
		infos.u32(uint32(len(t.dims)))
		for _, d := range t.dims {
			infos.u64(d)
		}
		infos.u32(0) // GGML_TYPE_F32
		infos.u64(uint64(data.Len()))
		for _, f := range t.data {
			var b [4]byte
			binary.LittleEndian.PutUint32(b[:], math.Float32bits(f))
			data.Write(b[:])
		}
	}

	var out ggufWriter
	out.WriteString("GGUF")
	out.u32(3)
	out.u64(uint64(len(tensors)))
	out.u64(uint64(entries))
	out.Write(meta.Bytes())
	out.Write(infos.Bytes())
	var all bytes.Buffer
	all.Write(out.Bytes())
	pad(&all)
	all.Write(data.Bytes())
	return all.Bytes()
}

// writeTinyGGUF writes the tiny model to path.
func writeTinyGGUF(t testing.TB, path string) {
	t.Helper()
	if err := os.WriteFile(path, tinyGGUF(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestTinyGGUFLayout checks the container of the generated model without llama.cpp: the magic, the
// version, the counts and the alignment of the data section.
func TestTinyGGUFLayout(t *testing.T) {
	b := tinyGGUF()
	if string(b[:4]) != "GGUF" || binary.LittleEndian.Uint32(b[4:8]) != 3 {
		t.Fatalf("header % x", b[:8])
	}
	tensors, entries := binary.LittleEndian.Uint64(b[8:16]), binary.LittleEndian.Uint64(b[16:24])
	if tensors != 12 || entries != 19 {
		t.Errorf("%d tensors and %d entries", tensors, entries)
	}
	if len(b)%4 != 0 || len(b) < 1000 {
		t.Errorf("size %d", len(b))
	}
	if !bytes.Equal(b, tinyGGUF()) {
		t.Error("the model is not deterministic")
	}
}

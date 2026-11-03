package piper

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

func TestStreamingHeaderFor22050Hz(t *testing.T) {
	h, err := StreamingHeader(22050)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != HeaderSize || HeaderSize != 44 {
		t.Fatalf("header is %d bytes, want 44", len(h))
	}
	want := []byte{
		'R', 'I', 'F', 'F', 0xFF, 0xFF, 0xFF, 0xFF, // RIFF, size unknown
		'W', 'A', 'V', 'E',
		'f', 'm', 't', ' ', 16, 0, 0, 0, // fmt chunk of 16 bytes
		1, 0, // PCM
		1, 0, // mono
		0x22, 0x56, 0, 0, // 22050 Hz
		0x44, 0xAC, 0, 0, // byte rate 44100
		2, 0, // block align
		16, 0, // bits per sample
		'd', 'a', 't', 'a', 0xFF, 0xFF, 0xFF, 0xFF, // data, size unknown
	}
	if !bytes.Equal(h, want) {
		t.Errorf("header\n got %x\nwant %x", h, want)
	}
	info, err := ParseHeader(h)
	if err != nil || info.SampleRate != 22050 || info.DataBytes != -1 {
		t.Errorf("ParseHeader = %+v, %v", info, err)
	}
}

func TestHeaderOfAFinishedFile(t *testing.T) {
	h, err := Header(16000, 32000)
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32(h[4:]); got != 36+32000 {
		t.Errorf("RIFF size = %d, want %d", got, 36+32000)
	}
	info, err := ParseHeader(h)
	if err != nil || info.SampleRate != 16000 || info.DataBytes != 32000 {
		t.Errorf("ParseHeader = %+v, %v", info, err)
	}
	for _, n := range []int{-2, 3, math.MaxUint32} {
		if _, err := Header(16000, n); !errors.Is(err, ErrBadWAV) {
			t.Errorf("Header(_, %d) = %v, want ErrBadWAV", n, err)
		}
	}
}

func TestHeaderRefusesRatesOutsideBounds(t *testing.T) {
	for _, r := range []int{-1, 0, 7999, 48001, 1 << 30} {
		if _, err := StreamingHeader(r); !errors.Is(err, ErrBadWAV) {
			t.Errorf("rate %d: %v, want ErrBadWAV", r, err)
		}
	}
	for _, r := range []int{8000, 16000, 22050, 44100, 48000} {
		if _, err := StreamingHeader(r); err != nil {
			t.Errorf("rate %d: %v", r, err)
		}
	}
}

func TestParseHeaderRejectsMalformedHeaders(t *testing.T) {
	good, _ := StreamingHeader(22050)
	mutate := func(f func(b []byte)) []byte {
		b := append([]byte(nil), good...)
		f(b)
		return b
	}
	cases := map[string][]byte{
		"empty":            nil,
		"short":            good[:43],
		"not riff":         mutate(func(b []byte) { copy(b, "RIFX") }),
		"not wave":         mutate(func(b []byte) { copy(b[8:], "AVI ") }),
		"no fmt":           mutate(func(b []byte) { copy(b[12:], "junk") }),
		"fmt size":         mutate(func(b []byte) { b[16] = 18 }),
		"float":            mutate(func(b []byte) { b[20] = 3 }),
		"stereo":           mutate(func(b []byte) { b[22] = 2 }),
		"8 bit":            mutate(func(b []byte) { b[34] = 8 }),
		"block align":      mutate(func(b []byte) { b[32] = 4 }),
		"rate zero":        mutate(func(b []byte) { binary.LittleEndian.PutUint32(b[24:], 0) }),
		"rate huge":        mutate(func(b []byte) { binary.LittleEndian.PutUint32(b[24:], 0xFFFFFFFF) }),
		"byte rate":        mutate(func(b []byte) { binary.LittleEndian.PutUint32(b[28:], 1) }),
		"data chunk moved": mutate(func(b []byte) { copy(b[36:], "LIST") }),
	}
	for name, b := range cases {
		if _, err := ParseHeader(b); !errors.Is(err, ErrBadWAV) {
			t.Errorf("%s: err = %v, want ErrBadWAV", name, err)
		}
	}
}

func FuzzParseHeader(f *testing.F) {
	good, _ := StreamingHeader(22050)
	f.Add(good)
	f.Add([]byte("RIFF"))
	f.Add(make([]byte, 64))
	f.Fuzz(func(t *testing.T, b []byte) {
		info, err := ParseHeader(b) // must never panic, and never read past 44 bytes
		if err == nil && (info.SampleRate < MinSampleRate || info.SampleRate > MaxSampleRate) {
			t.Fatalf("accepted rate %d", info.SampleRate)
		}
	})
}

// sine returns n samples of a sine at the given amplitude (0..1 of full scale) as s16le.
func sine(n int, amp float64) []byte {
	b := make([]byte, 2*n)
	for i := 0; i < n; i++ {
		v := int16(amp * 32767 * math.Sin(2*math.Pi*440*float64(i)/22050))
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return b
}

func TestPCMStats(t *testing.T) {
	secs, rms := PCMStats(sine(22050, 1), 22050)
	if math.Abs(secs-1) > 1e-9 {
		t.Errorf("duration = %v, want 1 s", secs)
	}
	// A full-scale sine has an RMS of 1/sqrt(2): about -3.01 dBFS.
	if math.Abs(rms-(-3.0103)) > 0.05 {
		t.Errorf("rms = %v dBFS, want about -3.01", rms)
	}
	if _, rms := PCMStats(sine(22050, 0.01), 22050); rms > -40 || rms < -45 {
		t.Errorf("a 1%% sine is about -43 dBFS, got %v", rms)
	}
	if secs, rms := PCMStats(make([]byte, 44100), 22050); secs != 1 || !math.IsInf(rms, -1) {
		t.Errorf("silence: %v s, %v dBFS", secs, rms)
	}
	if secs, rms := PCMStats(nil, 22050); secs != 0 || !math.IsInf(rms, -1) {
		t.Errorf("empty: %v, %v", secs, rms)
	}
	// A trailing odd byte is not a sample.
	if secs, _ := PCMStats(append(sine(22050, 1), 0x7f), 22050); secs != 1 {
		t.Errorf("odd byte counted: %v", secs)
	}
	if secs, _ := PCMStats(sine(10, 1), 0); secs != 0 {
		t.Errorf("rate 0: %v", secs)
	}
}

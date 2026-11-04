package whisper

import (
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

// wavSpec builds a WAV for the tests.
type wavSpec struct {
	tag      uint16
	channels int
	rate     int
	bits     int
	data     []byte
	// declare overrides the data chunk size in the header when non-zero.
	declare uint32
	// extensible writes WAVE_FORMAT_EXTENSIBLE with tag as the sub-format.
	extensible bool
	// extra is an unknown chunk written before data (odd length tests word alignment).
	extra []byte
}

func (s wavSpec) bytes() []byte {
	le := binary.LittleEndian
	var fmtc []byte
	if s.extensible {
		fmtc = make([]byte, 40)
		le.PutUint16(fmtc[0:], wavFormatExtensible)
		le.PutUint16(fmtc[16:], 22)
		le.PutUint16(fmtc[24:], s.tag)
	} else {
		fmtc = make([]byte, 16)
		le.PutUint16(fmtc[0:], s.tag)
	}
	le.PutUint16(fmtc[2:], uint16(s.channels))
	le.PutUint32(fmtc[4:], uint32(s.rate))
	le.PutUint16(fmtc[14:], uint16(s.bits))
	b := []byte("RIFF\x00\x00\x00\x00WAVE")
	b = append(b, "fmt "...)
	b = le.AppendUint32(b, uint32(len(fmtc)))
	b = append(b, fmtc...)
	if s.extra != nil {
		b = append(b, "LIST"...)
		b = le.AppendUint32(b, uint32(len(s.extra)))
		b = append(b, s.extra...)
		if len(s.extra)%2 == 1 {
			b = append(b, 0)
		}
	}
	b = append(b, "data"...)
	size := uint32(len(s.data))
	if s.declare != 0 {
		size = s.declare
	}
	b = le.AppendUint32(b, size)
	b = append(b, s.data...)
	le.PutUint32(b[4:], uint32(len(b)-8))
	return b
}

func pcm16(samples ...int16) []byte {
	b := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(s))
	}
	return b
}

func f32(samples ...float32) []byte {
	b := make([]byte, 4*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(s))
	}
	return b
}

func near(a, b float32) bool { return math.Abs(float64(a-b)) < 1e-4 }

func TestDecodeWAV(t *testing.T) {
	tests := []struct {
		name     string
		wav      []byte
		max      float64
		err      error
		rate     int
		channels int
		samples  []float32
	}{
		{"pcm16 mono", wavSpec{tag: 1, channels: 1, rate: 16000, bits: 16, data: pcm16(0, 16384, -32768)}.bytes(), 0, nil, 16000, 1, []float32{0, 0.5, -1}},
		{"float32 mono", wavSpec{tag: 3, channels: 1, rate: 8000, bits: 32, data: f32(0.25, -0.5)}.bytes(), 0, nil, 8000, 1, []float32{0.25, -0.5}},
		{"extensible pcm16", wavSpec{tag: 1, channels: 1, rate: 16000, bits: 16, data: pcm16(16384), extensible: true}.bytes(), 0, nil, 16000, 1, []float32{0.5}},
		{"extensible float", wavSpec{tag: 3, channels: 1, rate: 16000, bits: 32, data: f32(0.5), extensible: true}.bytes(), 0, nil, 16000, 1, []float32{0.5}},
		{"stereo kept interleaved", wavSpec{tag: 1, channels: 2, rate: 44100, bits: 16, data: pcm16(16384, 0, -16384, 0)}.bytes(), 0, nil, 44100, 2, []float32{0.5, 0, -0.5, 0}},
		{"unknown odd chunk skipped", wavSpec{tag: 1, channels: 1, rate: 16000, bits: 16, data: pcm16(16384), extra: []byte("abc")}.bytes(), 0, nil, 16000, 1, []float32{0.5}},
		{"declared size larger than body is truncated", wavSpec{tag: 1, channels: 1, rate: 16000, bits: 16, data: pcm16(16384, 8192), declare: 0xFFFFFFFF}.bytes(), 0, nil, 16000, 1, []float32{0.5, 0.25}},
		{"huge declared size, one trailing odd byte", wavSpec{tag: 1, channels: 1, rate: 16000, bits: 16, data: append(pcm16(16384), 7), declare: 0x7FFFFFFF}.bytes(), 0, nil, 16000, 1, []float32{0.5}},
		{"8-bit rejected", wavSpec{tag: 1, channels: 1, rate: 16000, bits: 8, data: []byte{1, 2, 3}}.bytes(), 0, ErrUnsupported, 0, 0, nil},
		{"24-bit rejected", wavSpec{tag: 1, channels: 1, rate: 16000, bits: 24, data: []byte{1, 2, 3}}.bytes(), 0, ErrUnsupported, 0, 0, nil},
		{"float64 rejected", wavSpec{tag: 3, channels: 1, rate: 16000, bits: 64, data: make([]byte, 8)}.bytes(), 0, ErrUnsupported, 0, 0, nil},
		{"mu-law rejected", wavSpec{tag: 7, channels: 1, rate: 8000, bits: 8, data: []byte{1}}.bytes(), 0, ErrUnsupported, 0, 0, nil},
		{"3 channels rejected", wavSpec{tag: 1, channels: 3, rate: 16000, bits: 16, data: pcm16(1, 2, 3)}.bytes(), 0, ErrUnsupported, 0, 0, nil},
		{"0 channels rejected", wavSpec{tag: 1, channels: 0, rate: 16000, bits: 16, data: pcm16(1)}.bytes(), 0, ErrUnsupported, 0, 0, nil},
		{"zero rate rejected", wavSpec{tag: 1, channels: 1, rate: 0, bits: 16, data: pcm16(1)}.bytes(), 0, ErrUnsupported, 0, 0, nil},
		{"empty data", wavSpec{tag: 1, channels: 1, rate: 16000, bits: 16}.bytes(), 0, ErrEmptyAudio, 0, 0, nil},
		{"too long", wavSpec{tag: 1, channels: 1, rate: 100, bits: 16, data: make([]byte, 2*300)}.bytes(), 2, ErrTooLong, 0, 0, nil},
		{"exactly the limit", wavSpec{tag: 1, channels: 1, rate: 100, bits: 16, data: make([]byte, 2*200)}.bytes(), 2, nil, 100, 1, make([]float32, 200)},
		{"not riff", []byte("this is an mp3 or something"), 0, ErrNotWAV, 0, 0, nil},
		{"empty body", nil, 0, ErrNotWAV, 0, 0, nil},
		{"truncated header", wavSpec{tag: 1, channels: 1, rate: 16000, bits: 16, data: pcm16(1)}.bytes()[:30], 0, ErrNotWAV, 0, 0, nil},
		{"only RIFF header", []byte("RIFF\x00\x00\x00\x00WAVE"), 0, ErrNotWAV, 0, 0, nil},
		{"data before fmt", []byte("RIFF\x00\x00\x00\x00WAVEdata\x02\x00\x00\x00\x00\x00fmt \x10\x00\x00\x00"), 0, ErrNotWAV, 0, 0, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, err := DecodeWAV(tc.wav, tc.max)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("err = %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if a.Rate != tc.rate || a.Channels != tc.channels || len(a.Samples) != len(tc.samples) {
				t.Fatalf("got rate %d ch %d n %d", a.Rate, a.Channels, len(a.Samples))
			}
			for i, w := range tc.samples {
				if !near(a.Samples[i], w) {
					t.Errorf("sample %d = %v, want %v", i, a.Samples[i], w)
				}
			}
		})
	}
}

func TestDownmixAveragesChannels(t *testing.T) {
	a := Audio{Rate: 16000, Channels: 2, Samples: []float32{1, 0, 0.5, -0.5, -1, -1}}
	got := a.Mono()
	want := []float32{0.5, 0, -1}
	for i := range want {
		if !near(got[i], want[i]) {
			t.Errorf("mono[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestFloatSamplesAreClamped(t *testing.T) {
	b := wavSpec{tag: 3, channels: 1, rate: 16000, bits: 32, data: f32(5, -5, float32(math.NaN()))}.bytes()
	a, err := DecodeWAV(b, 0)
	if err != nil {
		t.Fatal(err)
	}
	if a.Samples[0] != 1 || a.Samples[1] != -1 || a.Samples[2] != 0 {
		t.Fatalf("samples = %v", a.Samples)
	}
}

func constant(n int, v float32) []float32 {
	s := make([]float32, n)
	for i := range s {
		s[i] = v
	}
	return s
}

func TestSilenceLevel(t *testing.T) {
	tests := []struct {
		name    string
		samples []float32
		silent  bool
	}{
		{"zeros", make([]float32, 100), true},
		{"empty", nil, true},
		{"-70 dBFS hiss", constant(100, 0.0003), true},
		{"-50 dBFS tone", constant(100, 0.003), false},
		{"loud", constant(100, 0.5), false},
	}
	for _, tc := range tests {
		if got := RMSdBFS(tc.samples) < SilenceDBFS; got != tc.silent {
			t.Errorf("%s: silent = %v (%.1f dBFS)", tc.name, got, RMSdBFS(tc.samples))
		}
	}
}

func TestEncodeWAV16RoundTrip(t *testing.T) {
	in := []int16{0, 1000, -1000, 32767, -32768}
	a, err := DecodeWAV(EncodeWAV16(in, 16000), 0)
	if err != nil {
		t.Fatal(err)
	}
	if a.Rate != 16000 || a.Channels != 1 || len(a.Samples) != len(in) {
		t.Fatalf("round trip = %+v", a)
	}
	for i, s := range ToPCM16(a.Samples) {
		if s != in[i] {
			t.Errorf("sample %d = %d, want %d", i, s, in[i])
		}
	}
}

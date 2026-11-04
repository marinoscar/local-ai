package whisper

import (
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

// ---- builders ---------------------------------------------------------------------------------

func chunk(id string, body []byte) []byte {
	b := append([]byte(id), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(body)))
	b = append(b, body...)
	if len(body)%2 == 1 {
		b = append(b, 0)
	}
	return b
}

func riff(chunks ...[]byte) []byte {
	b := []byte("RIFF\x00\x00\x00\x00WAVE")
	for _, c := range chunks {
		b = append(b, c...)
	}
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	return b
}

func fmtBody(tag uint16, ch, rate, bits int) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint16(b[0:], tag)
	binary.LittleEndian.PutUint16(b[2:], uint16(ch))
	binary.LittleEndian.PutUint32(b[4:], uint32(rate))
	align := ch * bits / 8
	binary.LittleEndian.PutUint32(b[8:], uint32(rate*align))
	binary.LittleEndian.PutUint16(b[12:], uint16(align))
	binary.LittleEndian.PutUint16(b[14:], uint16(bits))
	return b
}

func extFmtBody(sub uint16, ch, rate, bits int) []byte {
	b := fmtBody(formatExtensible, ch, rate, bits)
	ext := make([]byte, 24)
	binary.LittleEndian.PutUint16(ext[0:], 22)
	binary.LittleEndian.PutUint16(ext[2:], uint16(bits))
	binary.LittleEndian.PutUint16(ext[8:], sub)
	copy(ext[10:], extensibleSuffix[:])
	return append(b, ext...)
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

func reasonOf(t *testing.T, err error) string {
	t.Helper()
	var fe *FormatError
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v, want a FormatError", err)
	}
	return fe.Reason
}

// ---- tests ------------------------------------------------------------------------------------

func TestDecodeWAVAccepted(t *testing.T) {
	cases := []struct {
		name     string
		wav      []byte
		rate     int
		channels int
		want     []float32
	}{
		{"pcm16 mono", riff(chunk("fmt ", fmtBody(1, 1, 16000, 16)), chunk("data", pcm16(16384, -16384, 0))), 16000, 1, []float32{0.5, -0.5, 0}},
		{"float32 mono", riff(chunk("fmt ", fmtBody(3, 1, 22050, 32)), chunk("data", f32(0.25, -1.5, float32(math.NaN())))), 22050, 1, []float32{0.25, -1, 0}},
		{"extensible pcm16", riff(chunk("fmt ", extFmtBody(1, 1, 48000, 16)), chunk("data", pcm16(8192))), 48000, 1, []float32{0.25}},
		{"extensible float32", riff(chunk("fmt ", extFmtBody(3, 1, 8000, 32)), chunk("data", f32(0.5))), 8000, 1, []float32{0.5}},
		{"stereo downmix averages", riff(chunk("fmt ", fmtBody(1, 2, 16000, 16)), chunk("data", pcm16(16384, 0, -16384, -16384))), 16000, 2, []float32{0.25, -0.5}},
		{"unknown chunks and padding skipped", riff(chunk("LIST", []byte("abc")), chunk("fmt ", fmtBody(1, 1, 16000, 16)), chunk("junk", []byte{1}), chunk("data", pcm16(16384))), 16000, 1, []float32{0.5}},
		{"data size larger than the body is cut", func() []byte {
			b := riff(chunk("fmt ", fmtBody(1, 1, 16000, 16)), chunk("data", pcm16(16384, 16384)))
			binary.LittleEndian.PutUint32(b[len(b)-8:], 0xFFFFFFFF) // a streaming header
			return b
		}(), 16000, 1, []float32{0.5, 0.5}},
		{"a partial frame at the end is dropped", riff(chunk("fmt ", fmtBody(1, 1, 16000, 16)), chunk("data", append(pcm16(16384), 7))), 16000, 1, []float32{0.5}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, err := DecodeWAV(c.wav, 0)
			if err != nil {
				t.Fatal(err)
			}
			if a.Rate != c.rate || a.Channels != c.channels || len(a.Samples) != len(c.want) {
				t.Fatalf("got rate %d ch %d n %d", a.Rate, a.Channels, len(a.Samples))
			}
			for i, w := range c.want {
				if math.Abs(float64(a.Samples[i]-w)) > 1e-6 {
					t.Errorf("sample %d = %v, want %v", i, a.Samples[i], w)
				}
			}
		})
	}
}

func TestDecodeWAVRejected(t *testing.T) {
	good := riff(chunk("fmt ", fmtBody(1, 1, 16000, 16)), chunk("data", pcm16(1, 2, 3)))
	huge := riff(chunk("fmt ", fmtBody(1, 1, 16000, 16)))
	huge = append(huge, "data\xff\xff\xff\xff"...) // declared 4 GiB, nothing behind it
	fmtHuge := []byte("RIFF\x00\x00\x00\x00WAVEfmt \xff\xff\xff\xff")
	cases := []struct {
		name   string
		wav    []byte
		reason string
	}{
		{"empty", nil, ReasonNotWAV},
		{"text", []byte("hello, this is not a recording"), ReasonNotWAV},
		{"riff without wave", append([]byte("RIFF\x00\x00\x00\x00AVI "), good[12:]...), ReasonNotWAV},
		{"truncated header", good[:20], ReasonTruncated},
		{"truncated in a chunk header", good[:15], ReasonNoFormat},
		{"fmt chunk longer than the body", fmtHuge, ReasonTruncated},
		{"huge declared data size, no data", huge, ReasonEmpty},
		{"8-bit pcm", riff(chunk("fmt ", fmtBody(1, 1, 16000, 8)), chunk("data", []byte{1, 2})), ReasonEncoding},
		{"24-bit pcm", riff(chunk("fmt ", fmtBody(1, 1, 16000, 24)), chunk("data", make([]byte, 6))), ReasonEncoding},
		{"float64", riff(chunk("fmt ", fmtBody(3, 1, 16000, 64)), chunk("data", make([]byte, 8))), ReasonEncoding},
		{"mu-law", riff(chunk("fmt ", fmtBody(7, 1, 8000, 8)), chunk("data", []byte{1})), ReasonEncoding},
		{"extensible with another sub-format", riff(chunk("fmt ", extFmtBody(2, 1, 16000, 16)), chunk("data", pcm16(1))), ReasonEncoding},
		{"three channels", riff(chunk("fmt ", fmtBody(1, 3, 16000, 16)), chunk("data", pcm16(1, 2, 3))), ReasonChannels},
		{"zero channels", riff(chunk("fmt ", fmtBody(1, 0, 16000, 16)), chunk("data", pcm16(1))), ReasonChannels},
		{"rate zero", riff(chunk("fmt ", fmtBody(1, 1, 0, 16)), chunk("data", pcm16(1))), ReasonRate},
		{"absurd rate", riff(chunk("fmt ", fmtBody(1, 1, 4000000, 16)), chunk("data", pcm16(1))), ReasonRate},
		{"short fmt chunk", riff(chunk("fmt ", make([]byte, 10)), chunk("data", pcm16(1))), ReasonBadFormat},
		{"data before fmt", riff(chunk("data", pcm16(1)), chunk("fmt ", fmtBody(1, 1, 16000, 16))), ReasonNoFormat},
		{"fmt only", riff(chunk("fmt ", fmtBody(1, 1, 16000, 16))), ReasonNoData},
		{"no samples", riff(chunk("fmt ", fmtBody(1, 1, 16000, 16)), chunk("data", nil)), ReasonEmpty},
		{"one byte of data", riff(chunk("fmt ", fmtBody(1, 1, 16000, 16)), chunk("data", []byte{1})), ReasonEmpty},
		{"wrong block align", func() []byte {
			f := fmtBody(1, 1, 16000, 16)
			binary.LittleEndian.PutUint16(f[12:], 4)
			return riff(chunk("fmt ", f), chunk("data", pcm16(1)))
		}(), ReasonBadSampling},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := DecodeWAV(c.wav, 0)
			if got := reasonOf(t, err); got != c.reason {
				t.Errorf("reason = %s, want %s", got, c.reason)
			}
		})
	}
}

func TestDecodeWAVTooLong(t *testing.T) {
	wav := riff(chunk("fmt ", fmtBody(1, 1, 8000, 16)), chunk("data", make([]byte, 2*8000*3))) // 3 s
	if _, err := DecodeWAV(wav, 3); err != nil {
		t.Fatalf("exactly the limit: %v", err)
	}
	_, err := DecodeWAV(wav, 2.9)
	if got := reasonOf(t, err); got != ReasonTooLong {
		t.Errorf("reason = %s, want %s", got, ReasonTooLong)
	}
}

func TestAudioLevels(t *testing.T) {
	loud := Audio{Samples: []float32{0.5, -0.5, 0.5, -0.5}, Rate: 4}
	if db := loud.RMSDBFS(); math.Abs(db-(-6.02)) > 0.01 {
		t.Errorf("rms = %.2f dBFS, want about -6.02", db)
	}
	if loud.Seconds() != 1 {
		t.Errorf("seconds = %v", loud.Seconds())
	}
	if db := (Audio{Samples: make([]float32, 10), Rate: 16000}).RMSDBFS(); !math.IsInf(db, -1) {
		t.Errorf("digital silence = %v dBFS", db)
	}
	quiet := Audio{Samples: []float32{0.0005, -0.0005}, Rate: 16000} // -66 dBFS
	if quiet.RMSDBFS() >= SilenceDBFS {
		t.Errorf("a -66 dBFS recording is not silent: %v", quiet.RMSDBFS())
	}
}

func TestEncodeWAV16RoundTrips(t *testing.T) {
	pcm := []int16{0, 1, -1, 32767, -32768}
	a, err := DecodeWAV(EncodeWAV16(pcm, WhisperRate), 0)
	if err != nil {
		t.Fatal(err)
	}
	if a.Rate != WhisperRate || a.Channels != 1 || len(a.Samples) != len(pcm) {
		t.Fatalf("got %+v", a)
	}
	for i, s := range pcm {
		if got := toPCM16(float64(a.Samples[i])); got != s {
			t.Errorf("sample %d = %d, want %d", i, got, s)
		}
	}
}

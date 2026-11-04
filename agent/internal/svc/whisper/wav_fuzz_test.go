package whisper

import "testing"

// FuzzDecodeWAV: no input makes the decoder panic, read past its input or return more samples than the
// bytes can hold. Run it for longer with: go test -run '^$' -fuzz FuzzDecodeWAV -fuzztime 60s .
func FuzzDecodeWAV(f *testing.F) {
	f.Add(riff(chunk("fmt ", fmtBody(1, 1, 16000, 16)), chunk("data", pcm16(1, -2, 3))))
	f.Add(riff(chunk("fmt ", fmtBody(3, 2, 44100, 32)), chunk("data", f32(0.5, -0.5, 1, 2))))
	f.Add(riff(chunk("fmt ", extFmtBody(3, 1, 8000, 32)), chunk("data", f32(0.5))))
	f.Add([]byte("RIFF\x00\x00\x00\x00WAVEdata\xff\xff\xff\xff"))
	f.Add([]byte("RIFF\x00\x00\x00\x00WAVEfmt \xff\xff\xff\xff"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		a, err := DecodeWAV(b, 120)
		if err != nil {
			return
		}
		if len(a.Samples)*2 > len(b) {
			t.Fatalf("%d samples from %d bytes", len(a.Samples), len(b))
		}
		if a.Rate < 1 || a.Rate > MaxSampleRate || a.Channels < 1 || a.Channels > MaxChannels {
			t.Fatalf("bad format %+v", a)
		}
		_ = a.RMSDBFS()
		if a.Seconds() <= 120 {
			// A recording this short is resampled without trouble.
			if n := len(Resample16k(a.Samples, a.Rate)); n < 0 {
				t.Fatal(n)
			}
		}
	})
}

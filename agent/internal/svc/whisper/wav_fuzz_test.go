package whisper

import "testing"

// FuzzDecodeWAV: no input may panic, over-read or decode more samples than the body holds.
func FuzzDecodeWAV(f *testing.F) {
	f.Add(wavSpec{tag: 1, channels: 1, rate: 16000, bits: 16, data: pcm16(1, -1, 300)}.bytes())
	f.Add(wavSpec{tag: 3, channels: 2, rate: 44100, bits: 32, data: f32(0.5, -0.5), extensible: true}.bytes())
	f.Add(wavSpec{tag: 1, channels: 1, rate: 16000, bits: 16, data: pcm16(1), declare: 0xFFFFFFFF, extra: []byte("x")}.bytes())
	f.Add([]byte("RIFF\xff\xff\xff\xffWAVEfmt \xff\xff\xff\xff"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		a, err := DecodeWAV(b, 120)
		if err != nil {
			return
		}
		if len(a.Samples)*2 > len(b) {
			t.Fatalf("decoded %d samples from %d bytes", len(a.Samples), len(b))
		}
		if a.Channels < 1 || a.Channels > 2 || len(a.Samples)%a.Channels != 0 {
			t.Fatalf("bad shape: %d channels, %d samples", a.Channels, len(a.Samples))
		}
		mono := Resample(a.Mono(), a.Rate, WhisperRate)
		_ = EncodeWAV16(ToPCM16(mono), WhisperRate)
	})
}

package whisper

import (
	"math"
	"testing"
)

func tone(freq float64, rate int, seconds float64, amp float64) []float32 {
	n := int(float64(rate) * seconds)
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(amp * math.Sin(2*math.Pi*freq*float64(i)/float64(rate)))
	}
	return out
}

func rmsOf(pcm []int16, from, to int) float64 {
	var sum float64
	for _, s := range pcm[from:to] {
		v := float64(s) / 32768
		sum += v * v
	}
	return math.Sqrt(sum / float64(to-from))
}

func TestResampleOutputLength(t *testing.T) {
	cases := []struct{ rate, in, want int }{
		{44100, 44100, 16000}, // 1 s
		{48000, 48000, 16000},
		{8000, 8000, 16000},
		{16000, 12345, 12345},
		{22050, 22050 * 3, 48000},
		{44100, 0, 0},
	}
	for _, c := range cases {
		if got := len(Resample16k(make([]float32, c.in), c.rate)); got != c.want {
			t.Errorf("%d samples at %d Hz: got %d, want %d", c.in, c.rate, got, c.want)
		}
	}
	if Resample16k([]float32{0.1}, 0) != nil {
		t.Error("rate 0 must give nothing")
	}
}

func TestResampleKeepsSpeechBandAndRemovesAliases(t *testing.T) {
	// A 1 kHz tone at amplitude 0.5 has RMS 0.354; it must come out at about the same level, from
	// every common rate.
	for _, rate := range []int{8000, 16000, 22050, 44100, 48000} {
		out := Resample16k(tone(1000, rate, 1, 0.5), rate)
		got := rmsOf(out, 2000, 14000) // away from the edges
		if math.Abs(got-0.3536) > 0.02 {
			t.Errorf("%d Hz: rms %.4f, want about 0.354", rate, got)
		}
	}
	// A 12 kHz tone at 48 kHz is above the 8 kHz Nyquist limit of the output: it must be attenuated, not
	// folded back into the speech band at full level.
	out := Resample16k(tone(12000, 48000, 1, 0.5), 48000)
	if got := rmsOf(out, 2000, 14000); got > 0.18 {
		t.Errorf("12 kHz tone leaked through at rms %.4f", got)
	}
}

func TestResampleClipsInsteadOfWrapping(t *testing.T) {
	out := Resample16k([]float32{1, 1, 1, 1}, 16000)
	for _, s := range out {
		if s != 32767 {
			t.Fatalf("full scale = %d", s)
		}
	}
	if got := toPCM16(-2); got != -32768 {
		t.Errorf("clip low = %d", got)
	}
}

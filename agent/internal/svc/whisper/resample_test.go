package whisper

import (
	"math"
	"testing"
)

func tone(n, rate int, hz, amp float64) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(amp * math.Sin(2*math.Pi*hz*float64(i)/float64(rate)))
	}
	return out
}

func rms(s []float32) float64 {
	var sum float64
	for _, v := range s {
		sum += float64(v) * float64(v)
	}
	return math.Sqrt(sum / float64(len(s)))
}

func TestResampleLength(t *testing.T) {
	tests := []struct{ from, in, want int }{
		{44100, 44100, 16000},
		{48000, 48000, 16000},
		{8000, 8000, 16000},
		{16000, 1600, 1600},
		{22050, 22050, 16000},
		{44100, 10, 3},
		{44100, 0, 0},
	}
	for _, tc := range tests {
		got := Resample(make([]float32, tc.in), tc.from, WhisperRate)
		if len(got) != tc.want {
			t.Errorf("%d Hz, %d samples -> %d, want %d", tc.from, tc.in, len(got), tc.want)
		}
	}
}

func TestResampleKeepsSpeechBandAndRejectsAliases(t *testing.T) {
	// 1 kHz passes at about the same level; 12 kHz (above the new 8 kHz Nyquist) is removed.
	pass := Resample(tone(44100, 44100, 1000, 0.5), 44100, WhisperRate)
	if got := rms(pass[100 : len(pass)-100]); math.Abs(got-0.5/math.Sqrt2) > 0.02 {
		t.Errorf("1 kHz rms = %.3f, want about %.3f", got, 0.5/math.Sqrt2)
	}
	stop := Resample(tone(44100, 44100, 12000, 0.5), 44100, WhisperRate)
	if got := rms(stop[100 : len(stop)-100]); got > 0.05 {
		t.Errorf("12 kHz leaks through at rms %.3f", got)
	}
}

func TestResampleUpward(t *testing.T) {
	up := Resample(tone(8000, 8000, 500, 0.5), 8000, WhisperRate)
	if len(up) != 16000 {
		t.Fatalf("len = %d", len(up))
	}
	if got := rms(up[100 : len(up)-100]); math.Abs(got-0.5/math.Sqrt2) > 0.02 {
		t.Errorf("500 Hz rms = %.3f", got)
	}
}

func TestResampleSameRateIsIdentity(t *testing.T) {
	in := []float32{0.1, 0.2, 0.3}
	got := Resample(in, WhisperRate, WhisperRate)
	for i := range in {
		if got[i] != in[i] {
			t.Fatalf("got %v", got)
		}
	}
}

func TestToPCM16Clips(t *testing.T) {
	got := ToPCM16([]float32{0, 1, -1, 2, -2, 0.5})
	want := []int16{0, 32767, -32768, 32767, -32768, 16384}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

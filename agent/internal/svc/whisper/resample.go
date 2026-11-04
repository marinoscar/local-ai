package whisper

import "math"

// Resampling to the 16 kHz whisper.cpp takes (companion-services.md section 4.2.5, step 3): a
// windowed-sinc low-pass interpolator with a fixed 32 taps (16 on each side of the output instant).
// That is short for a hi-fi downsampler and plenty for speech.

const resampleHalfTaps = 16

// Resample16k converts mono samples at rate to 16 kHz PCM16. The number of output samples is
// floor(len(in) * 16000 / rate). A recording already at 16 kHz is only converted to integers.
func Resample16k(in []float32, rate int) []int16 {
	if rate <= 0 || len(in) == 0 {
		return nil
	}
	n := int(int64(len(in)) * WhisperRate / int64(rate))
	out := make([]int16, n)
	if rate == WhisperRate {
		for i := range out {
			out[i] = toPCM16(float64(in[i]))
		}
		return out
	}
	step := float64(rate) / WhisperRate // input samples per output sample
	// Below the input Nyquist limit of the output rate the kernel is a low-pass: its cutoff is the
	// smaller of the two Nyquist frequencies, as a fraction of the input's.
	cutoff := 1.0
	if rate > WhisperRate {
		cutoff = WhisperRate / float64(rate)
	}
	var w [2 * resampleHalfTaps]float64
	for i := range out {
		t := float64(i) * step
		base := int(math.Floor(t))
		var sum, norm float64
		for k := 0; k < 2*resampleHalfTaps; k++ {
			idx := base - resampleHalfTaps + 1 + k
			x := float64(idx) - t // distance of this input sample from the output instant
			w[k] = cutoff * sinc(cutoff*x) * hann(x/resampleHalfTaps)
			norm += w[k]
			if idx >= 0 && idx < len(in) {
				sum += w[k] * float64(in[idx])
			}
		}
		if norm != 0 {
			sum /= norm // unity gain at DC (samples outside the recording count as zero)
		}
		out[i] = toPCM16(sum)
	}
	return out
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	px := math.Pi * x
	return math.Sin(px) / px
}

// hann is the window over u in [-1, 1].
func hann(u float64) float64 {
	if u <= -1 || u >= 1 {
		return 0
	}
	return 0.5 * (1 + math.Cos(math.Pi*u))
}

func toPCM16(v float64) int16 {
	v = math.Round(v * 32768) // the inverse of the decoder (x / 32768)
	switch {
	case v > 32767:
		return 32767
	case v < -32768:
		return -32768
	}
	return int16(v)
}

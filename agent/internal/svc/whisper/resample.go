package whisper

import "math"

// WhisperRate is the sample rate whisper.cpp takes.
const WhisperRate = 16000

// sincHalfTaps is half of the fixed 32-tap kernel (companion-services.md section 4.2.5).
const sincHalfTaps = 16

// Resample converts mono samples from rate from to rate to with a Hann-windowed sinc of 32 taps at the
// lower of the two rates (a downsample is band limited by the same kernel, widened). The output has
// floor(len(in)*to/from) samples. Equal rates return in unchanged.
func Resample(in []float32, from, to int) []float32 {
	if from == to || len(in) == 0 || from <= 0 || to <= 0 {
		return in
	}
	n := int(int64(len(in)) * int64(to) / int64(from))
	out := make([]float32, n)
	ratio := float64(from) / float64(to) // input samples per output sample
	scale := math.Max(1, ratio)          // kernel width in input samples per tap
	half := sincHalfTaps * scale
	for i := range out {
		center := float64(i) * ratio
		lo := int(math.Ceil(center - half))
		hi := int(math.Floor(center + half))
		var acc, wsum float64
		for j := lo; j <= hi; j++ {
			x := (float64(j) - center) / scale
			k := sinc(x) * 0.5 * (1 + math.Cos(math.Pi*x/sincHalfTaps))
			wsum += k
			if j >= 0 && j < len(in) {
				acc += k * float64(in[j])
			}
		}
		if wsum != 0 {
			acc /= wsum // unit DC gain, also at the edges of the recording
		}
		out[i] = float32(acc)
	}
	return out
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	return math.Sin(math.Pi*x) / (math.Pi * x)
}

// ToPCM16 clips and rounds samples to 16-bit.
func ToPCM16(in []float32) []int16 {
	out := make([]int16, len(in))
	for i, s := range in {
		v := math.Round(float64(s) * 32768)
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		out[i] = int16(v)
	}
	return out
}

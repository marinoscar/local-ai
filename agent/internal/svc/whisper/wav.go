package whisper

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// WAV format tags (RIFF WAVEFORMATEX).
const (
	wavFormatPCM        = 0x0001
	wavFormatIEEEFloat  = 0x0003
	wavFormatExtensible = 0xFFFE
)

// Reasons a recording is refused; every one is LAI-SVC-014 (companion-services.md section 4.2.5).
var (
	ErrNotWAV      = errors.New("not a RIFF/WAVE file")
	ErrUnsupported = errors.New("unsupported WAV encoding")
	ErrEmptyAudio  = errors.New("the recording has no audio")
	ErrTooLong     = errors.New("the recording is longer than allowed")
	ErrSilent      = errors.New("the recording is silent")
)

// Audio is a decoded recording: interleaved samples in -1..1.
type Audio struct {
	Rate     int
	Channels int
	Samples  []float32 // interleaved, len is a multiple of Channels
}

// Frames is the number of sample frames.
func (a Audio) Frames() int {
	if a.Channels == 0 {
		return 0
	}
	return len(a.Samples) / a.Channels
}

// Seconds is the duration of the recording.
func (a Audio) Seconds() float64 {
	if a.Rate == 0 {
		return 0
	}
	return float64(a.Frames()) / float64(a.Rate)
}

// maxRate bounds the sample rate a header may claim; it keeps the arithmetic below safe.
const maxRate = 768000

// DecodeWAV parses a WAV file. It accepts WAVE_FORMAT_PCM 16-bit, WAVE_FORMAT_IEEE_FLOAT 32-bit and
// WAVE_FORMAT_EXTENSIBLE carrying either, with 1 or 2 channels and any rate. Everything else is
// ErrUnsupported (8-bit PCM included). Every chunk size is checked against the bytes left; unknown
// chunks are skipped; nothing is allocated from a declared size: a data chunk that declares more than
// the body holds (a recorder that streamed to a pipe) is truncated to the body. maxSeconds > 0 refuses
// a longer recording with ErrTooLong before any sample is converted.
func DecodeWAV(b []byte, maxSeconds float64) (Audio, error) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return Audio{}, ErrNotWAV
	}
	var (
		haveFmt  bool
		tag      uint16
		channels int
		rate     int
		bits     int
		data     []byte
		haveData bool
	)
	pos := 12
	for pos+8 <= len(b) && !haveData {
		id := string(b[pos : pos+4])
		size := uint64(binary.LittleEndian.Uint32(b[pos+4 : pos+8]))
		body := pos + 8
		left := uint64(len(b) - body)
		if size > left {
			if id != "data" {
				return Audio{}, ErrNotWAV // a truncated header chunk
			}
			size = left // streamed data chunk: take what is there
		}
		chunk := b[body : body+int(size)]
		switch id {
		case "fmt ":
			if len(chunk) < 16 {
				return Audio{}, ErrNotWAV
			}
			tag = binary.LittleEndian.Uint16(chunk[0:2])
			channels = int(binary.LittleEndian.Uint16(chunk[2:4]))
			rate = int(binary.LittleEndian.Uint32(chunk[4:8]))
			bits = int(binary.LittleEndian.Uint16(chunk[14:16]))
			if tag == wavFormatExtensible {
				if len(chunk) < 26 || binary.LittleEndian.Uint16(chunk[16:18]) < 22 {
					return Audio{}, ErrUnsupported
				}
				// The sub-format GUID starts with the real format tag.
				tag = binary.LittleEndian.Uint16(chunk[24:26])
			}
			haveFmt = true
		case "data":
			if !haveFmt {
				return Audio{}, ErrNotWAV
			}
			data, haveData = chunk, true
		}
		pos = body + int(size)
		if size%2 == 1 && pos < len(b) { // chunks are word aligned
			pos++
		}
	}
	if !haveFmt || !haveData {
		return Audio{}, ErrNotWAV
	}
	if channels < 1 || channels > 2 || rate < 1 || rate > maxRate {
		return Audio{}, ErrUnsupported
	}
	var width int
	switch {
	case tag == wavFormatPCM && bits == 16:
		width = 2
	case tag == wavFormatIEEEFloat && bits == 32:
		width = 4
	default:
		return Audio{}, fmt.Errorf("%w: format %#x, %d bits", ErrUnsupported, tag, bits)
	}
	frames := len(data) / (width * channels)
	if frames == 0 {
		return Audio{}, ErrEmptyAudio
	}
	if maxSeconds > 0 && float64(frames)/float64(rate) > maxSeconds {
		return Audio{}, ErrTooLong
	}
	out := make([]float32, frames*channels)
	if width == 2 {
		for i := range out {
			out[i] = float32(int16(binary.LittleEndian.Uint16(data[2*i:]))) / 32768
		}
	} else {
		for i := range out {
			v := math.Float32frombits(binary.LittleEndian.Uint32(data[4*i:]))
			switch {
			case v != v: // NaN
				v = 0
			case v > 1:
				v = 1
			case v < -1:
				v = -1
			}
			out[i] = v
		}
	}
	return Audio{Rate: rate, Channels: channels, Samples: out}, nil
}

// Mono averages the channels.
func (a Audio) Mono() []float32 {
	if a.Channels == 1 {
		return a.Samples
	}
	n := a.Frames()
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		var s float32
		for c := 0; c < a.Channels; c++ {
			s += a.Samples[i*a.Channels+c]
		}
		out[i] = s / float32(a.Channels)
	}
	return out
}

// RMSdBFS is the RMS level of samples in dB relative to full scale (-Inf for pure silence).
func RMSdBFS(samples []float32) float64 {
	if len(samples) == 0 {
		return math.Inf(-1)
	}
	var sum float64
	for _, s := range samples {
		sum += float64(s) * float64(s)
	}
	return 20 * math.Log10(math.Sqrt(sum/float64(len(samples))))
}

// SilenceDBFS is the level below which a recording counts as silent.
const SilenceDBFS = -60.0

// EncodeWAV16 wraps mono PCM16 samples at rate in a canonical 44-byte-header WAV.
func EncodeWAV16(pcm []int16, rate int) []byte {
	dataLen := len(pcm) * 2
	b := make([]byte, 44+dataLen)
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+dataLen))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], wavFormatPCM)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], uint32(rate))
	binary.LittleEndian.PutUint32(b[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(dataLen))
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(s))
	}
	return b
}

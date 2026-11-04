package whisper

import (
	"encoding/binary"
	"errors"
	"math"
)

// WAV decoding for POST /speech/stt (companion-services.md section 4.2.5, steps 2 to 4). The decoder is
// strict and works on a byte slice already in memory: every chunk size is checked against the bytes
// that remain, nothing is allocated from a declared size, and unknown chunks are skipped.

// WAVE_FORMAT tags the decoder accepts.
const (
	formatPCM        = 0x0001
	formatIEEEFloat  = 0x0003
	formatExtensible = 0xFFFE
)

// Limits of the decoder.
const (
	// MaxChannels is the most channels a recording may have (mono or stereo).
	MaxChannels = 2
	// MaxSampleRate is the highest rate accepted; anything above is not a microphone recording.
	MaxSampleRate = 384000
	// WhisperRate is the rate whisper.cpp takes: 16 kHz.
	WhisperRate = 16000
	// SilenceDBFS is the RMS below which a recording counts as silent.
	SilenceDBFS = -60.0
)

// Reasons of a FormatError (the details.reason of the 400).
const (
	ReasonNotWAV      = "not_wav"
	ReasonTruncated   = "truncated"
	ReasonNoFormat    = "no_format_chunk"
	ReasonBadFormat   = "bad_format_chunk"
	ReasonEncoding    = "unsupported_encoding"
	ReasonChannels    = "unsupported_channels"
	ReasonRate        = "unsupported_rate"
	ReasonNoData      = "no_data_chunk"
	ReasonEmpty       = "empty"
	ReasonTooLong     = "too_long"
	ReasonSilent      = "silent"
	ReasonBadSampling = "bad_block_align"
)

// FormatError is a recording that cannot be used; the route answers 400 LAI-SVC-014.
type FormatError struct {
	Reason string
	// Detail carries a number worth showing (the encoding tag, the channel count, the rate).
	Detail int
}

// Error implements error.
func (e *FormatError) Error() string { return "whisper: unusable recording: " + e.Reason }

// IsFormatError reports whether err is a FormatError.
func IsFormatError(err error) bool {
	var fe *FormatError
	return errors.As(err, &fe)
}

func bad(reason string) error { return &FormatError{Reason: reason} }

// Audio is a decoded recording: mono samples in [-1, 1] (channels averaged) at the original rate.
type Audio struct {
	Samples  []float32
	Rate     int
	Channels int
}

// Seconds is the length of the recording.
func (a Audio) Seconds() float64 {
	if a.Rate <= 0 {
		return 0
	}
	return float64(len(a.Samples)) / float64(a.Rate)
}

// RMSDBFS is the RMS level in dB full scale (-Inf for digital silence).
func (a Audio) RMSDBFS() float64 {
	if len(a.Samples) == 0 {
		return math.Inf(-1)
	}
	var sum float64
	for _, s := range a.Samples {
		sum += float64(s) * float64(s)
	}
	rms := math.Sqrt(sum / float64(len(a.Samples)))
	if rms == 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(rms)
}

// extensibleSuffix is the tail of the KSDATAFORMAT_SUBTYPE GUID: its first two bytes are the format
// tag, these fourteen are the same for PCM and IEEE float.
var extensibleSuffix = [14]byte{0x00, 0x00, 0x00, 0x00, 0x10, 0x00, 0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71}

type wavFormat struct {
	tag        uint16
	channels   int
	rate       int
	blockAlign int
	bits       int
}

// DecodeWAV decodes b. maxSeconds > 0 rejects a longer recording before its samples are converted.
// A declared data size larger than the bytes that follow is cut to what is there (a recorder that was
// stopped, or a streaming header with an unknown size); a partial frame at the end is dropped.
func DecodeWAV(b []byte, maxSeconds float64) (Audio, error) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return Audio{}, bad(ReasonNotWAV)
	}
	var f wavFormat
	haveFmt := false
	pos := 12
	for pos+8 <= len(b) {
		id := string(b[pos : pos+4])
		size := int64(binary.LittleEndian.Uint32(b[pos+4 : pos+8]))
		body := pos + 8
		remain := int64(len(b) - body)
		if size > remain {
			if id != "data" {
				return Audio{}, bad(ReasonTruncated)
			}
			size = remain
		}
		switch id {
		case "fmt ":
			var err error
			if f, err = parseFormat(b[body : body+int(size)]); err != nil {
				return Audio{}, err
			}
			haveFmt = true
		case "data":
			if !haveFmt {
				return Audio{}, bad(ReasonNoFormat)
			}
			return decodeData(b[body:body+int(size)], f, maxSeconds)
		}
		next := int64(body) + size + size&1 // chunks are padded to an even size
		if next > int64(len(b)) {
			break
		}
		pos = int(next)
	}
	if !haveFmt {
		return Audio{}, bad(ReasonNoFormat)
	}
	return Audio{}, bad(ReasonNoData)
}

func parseFormat(c []byte) (wavFormat, error) {
	if len(c) < 16 {
		return wavFormat{}, bad(ReasonBadFormat)
	}
	f := wavFormat{
		tag:        binary.LittleEndian.Uint16(c[0:2]),
		channels:   int(binary.LittleEndian.Uint16(c[2:4])),
		rate:       int(binary.LittleEndian.Uint32(c[4:8])),
		blockAlign: int(binary.LittleEndian.Uint16(c[12:14])),
		bits:       int(binary.LittleEndian.Uint16(c[14:16])),
	}
	if f.tag == formatExtensible {
		// cbSize (2), valid bits (2), channel mask (4), sub-format GUID (16): 24 bytes after bits.
		if len(c) < 40 || binary.LittleEndian.Uint16(c[16:18]) < 22 {
			return wavFormat{}, bad(ReasonBadFormat)
		}
		var suffix [14]byte
		copy(suffix[:], c[26:40])
		if suffix != extensibleSuffix {
			return wavFormat{}, &FormatError{Reason: ReasonEncoding, Detail: formatExtensible}
		}
		f.tag = binary.LittleEndian.Uint16(c[24:26])
	}
	switch {
	case f.tag == formatPCM && f.bits == 16:
	case f.tag == formatIEEEFloat && f.bits == 32:
	default:
		return wavFormat{}, &FormatError{Reason: ReasonEncoding, Detail: int(f.tag)}
	}
	if f.channels < 1 || f.channels > MaxChannels {
		return wavFormat{}, &FormatError{Reason: ReasonChannels, Detail: f.channels}
	}
	if f.rate < 1 || f.rate > MaxSampleRate {
		return wavFormat{}, &FormatError{Reason: ReasonRate, Detail: f.rate}
	}
	if f.blockAlign != f.channels*f.bits/8 {
		return wavFormat{}, bad(ReasonBadSampling)
	}
	return f, nil
}

func decodeData(d []byte, f wavFormat, maxSeconds float64) (Audio, error) {
	frames := len(d) / f.blockAlign
	if frames == 0 {
		return Audio{}, bad(ReasonEmpty)
	}
	if maxSeconds > 0 && float64(frames)/float64(f.rate) > maxSeconds {
		return Audio{}, bad(ReasonTooLong)
	}
	out := make([]float32, frames)
	sampleBytes := f.bits / 8
	for i := 0; i < frames; i++ {
		var sum float32
		for c := 0; c < f.channels; c++ {
			o := i*f.blockAlign + c*sampleBytes
			if f.tag == formatPCM {
				sum += float32(int16(binary.LittleEndian.Uint16(d[o:]))) / 32768
			} else {
				v := math.Float32frombits(binary.LittleEndian.Uint32(d[o:]))
				if v != v || math.IsInf(float64(v), 0) { // NaN and infinities are silence
					v = 0
				}
				sum += clamp(v)
			}
		}
		out[i] = sum / float32(f.channels)
	}
	return Audio{Samples: out, Rate: f.rate, Channels: f.channels}, nil
}

func clamp(v float32) float32 {
	switch {
	case v > 1:
		return 1
	case v < -1:
		return -1
	}
	return v
}

// EncodeWAV16 wraps mono PCM16 samples at rate in a 44-byte-header WAV.
func EncodeWAV16(pcm []int16, rate int) []byte {
	dataLen := len(pcm) * 2
	b := make([]byte, 44+dataLen)
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+dataLen))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], formatPCM)
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

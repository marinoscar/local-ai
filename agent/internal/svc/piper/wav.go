package piper

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// HeaderSize is the length of the WAV header the agent writes: RIFF chunk, "fmt " chunk of a plain
// PCM format, and the start of the "data" chunk.
const HeaderSize = 44

// Bounds of the sample rate accepted from a voice's .onnx.json (usually 22 050 Hz). Anything
// outside is a damaged config, not a voice, and is refused before it is put in a header.
const (
	MinSampleRate = 8000
	MaxSampleRate = 48000
)

// ErrBadWAV marks a header that is not the 16-bit mono PCM WAV this package writes.
var ErrBadWAV = errors.New("piper: malformed WAV")

// unknownSize is the RIFF and data size of a streamed WAV. The length of the audio is not known
// when the first byte is sent (the response is chunked and starts before synthesis ends), so both
// sizes are 0xFFFFFFFF, the convention for "until the end of the stream". Browsers (decodeAudioData,
// <audio>), aplay, sox and ffmpeg all read it.
const unknownSize = 0xFFFFFFFF

// StreamingHeader returns the 44-byte header of a mono 16-bit PCM stream at rate Hz whose length is
// unknown: RIFF and data sizes 0xFFFFFFFF. rate must be within MinSampleRate and MaxSampleRate.
func StreamingHeader(rate int) ([]byte, error) { return header(rate, unknownSize, unknownSize) }

// Header returns the 44-byte header of a finished mono 16-bit PCM file of dataBytes bytes (a whole
// number of samples). The deep check and the tests use it; the live response uses StreamingHeader.
func Header(rate int, dataBytes int) ([]byte, error) {
	if dataBytes < 0 || dataBytes%2 != 0 || int64(dataBytes) > math.MaxUint32-36 {
		return nil, fmt.Errorf("%w: %d data bytes", ErrBadWAV, dataBytes)
	}
	return header(rate, uint32(36+dataBytes), uint32(dataBytes))
}

func header(rate int, riffSize, dataSize uint32) ([]byte, error) {
	if rate < MinSampleRate || rate > MaxSampleRate {
		return nil, fmt.Errorf("%w: sample rate %d is outside %d..%d", ErrBadWAV, rate, MinSampleRate, MaxSampleRate)
	}
	h := make([]byte, HeaderSize)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], riffSize)
	copy(h[8:], "WAVE")
	copy(h[12:], "fmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)           // fmt chunk size
	binary.LittleEndian.PutUint16(h[20:], 1)            // PCM
	binary.LittleEndian.PutUint16(h[22:], 1)            // mono
	binary.LittleEndian.PutUint32(h[24:], uint32(rate)) // sample rate
	binary.LittleEndian.PutUint32(h[28:], uint32(rate)*2)
	binary.LittleEndian.PutUint16(h[32:], 2)  // block align
	binary.LittleEndian.PutUint16(h[34:], 16) // bits per sample
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], dataSize)
	return h, nil
}

// WAVInfo is what ParseHeader read.
type WAVInfo struct {
	SampleRate int
	// DataBytes is the size the header declares, or -1 when it is the "unknown" 0xFFFFFFFF of a
	// stream.
	DataBytes int64
}

// ParseHeader checks b (at least the first HeaderSize bytes of a file or stream) is exactly the
// header this package writes: RIFF/WAVE, a 16-byte PCM "fmt " chunk of one 16-bit channel with
// consistent byte rate and block align, a sample rate within bounds, and "data" next. It never reads
// past the 44 bytes and never allocates from a declared size, so a hostile header cannot hurt.
func ParseHeader(b []byte) (WAVInfo, error) {
	if len(b) < HeaderSize {
		return WAVInfo{}, fmt.Errorf("%w: %d bytes, a header is %d", ErrBadWAV, len(b), HeaderSize)
	}
	bad := func(why string) (WAVInfo, error) { return WAVInfo{}, fmt.Errorf("%w: %s", ErrBadWAV, why) }
	if string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return bad("not a RIFF/WAVE file")
	}
	if string(b[12:16]) != "fmt " || binary.LittleEndian.Uint32(b[16:]) != 16 {
		return bad("the fmt chunk is not the 16-byte PCM one")
	}
	if binary.LittleEndian.Uint16(b[20:]) != 1 {
		return bad("not PCM")
	}
	if binary.LittleEndian.Uint16(b[22:]) != 1 {
		return bad("not mono")
	}
	if binary.LittleEndian.Uint16(b[34:]) != 16 || binary.LittleEndian.Uint16(b[32:]) != 2 {
		return bad("not 16-bit samples")
	}
	rate := binary.LittleEndian.Uint32(b[24:])
	if rate < MinSampleRate || rate > MaxSampleRate {
		return bad(fmt.Sprintf("sample rate %d is outside %d..%d", rate, MinSampleRate, MaxSampleRate))
	}
	if binary.LittleEndian.Uint32(b[28:]) != rate*2 {
		return bad("byte rate does not match the sample rate")
	}
	if string(b[36:40]) != "data" {
		return bad("the data chunk does not follow fmt")
	}
	info := WAVInfo{SampleRate: int(rate), DataBytes: int64(binary.LittleEndian.Uint32(b[40:]))}
	if info.DataBytes == unknownSize {
		info.DataBytes = -1
	}
	return info, nil
}

// PCMStats measures raw s16le mono audio: its duration in seconds and its RMS level in dBFS
// (0 is full scale; digital silence is -Inf). A trailing odd byte is not a sample and is ignored.
func PCMStats(pcm []byte, rate int) (seconds, rmsDBFS float64) {
	n := len(pcm) / 2
	if n == 0 || rate <= 0 {
		return 0, math.Inf(-1)
	}
	var sum float64
	for i := 0; i < n; i++ {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) / 32768
		sum += v * v
	}
	rms := math.Sqrt(sum / float64(n))
	if rms == 0 {
		return float64(n) / float64(rate), math.Inf(-1)
	}
	return float64(n) / float64(rate), 20 * math.Log10(rms)
}

// voiceConfig is the part of a voice's .onnx.json the agent reads.
type voiceConfig struct {
	Audio struct {
		SampleRate int `json:"sample_rate"`
	} `json:"audio"`
}

package music

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
)

// The reference sample of a song (E11 contract section 3): WAV, MP3, FLAC or Ogg, at most 20 MiB and
// 60 seconds. The type comes from the file's first bytes, never from a name or a header the client
// sent; the duration from the container's own header. A file whose length cannot be read is refused:
// the engine would otherwise get an unbounded input.

// Sample limits.
const (
	MaxSampleBytes   = 20 << 20
	MaxSampleSeconds = 60.0
)

// Sample formats: the extension the file is stored with and its media type.
type SampleFormat struct {
	Ext  string
	Mime string
}

var (
	FormatWAV  = SampleFormat{"wav", "audio/wav"}
	FormatMP3  = SampleFormat{"mp3", "audio/mpeg"}
	FormatFLAC = SampleFormat{"flac", "audio/flac"}
	FormatOGG  = SampleFormat{"ogg", "audio/ogg"}
)

// Reasons a sample is refused (details.reason of LAI-SVC-154).
const (
	ReasonUnsupportedType = "unsupported_type"
	ReasonUnknownDuration = "unknown_duration"
	ReasonTooLong         = "too_long"
	ReasonTooLarge        = "too_large"
	ReasonEmpty           = "empty"
)

// SampleError is a sample that is refused, with its reason.
type SampleError struct{ Reason string }

func (e *SampleError) Error() string { return "music: sample refused: " + e.Reason }

// SampleInfo is what inspecting a sample found.
type SampleInfo struct {
	Format   SampleFormat
	Seconds  float64
	Bytes    int64
	PathOnFS string
}

// InspectSample reads the type and the duration of the file at path and checks the limits.
func InspectSample(path string) (SampleInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return SampleInfo{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return SampleInfo{}, err
	}
	size := st.Size()
	if size == 0 {
		return SampleInfo{}, &SampleError{ReasonEmpty}
	}
	if size > MaxSampleBytes {
		return SampleInfo{}, &SampleError{ReasonTooLarge}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return SampleInfo{}, err
	}
	format, secs, err := probe(data)
	if err != nil {
		return SampleInfo{}, err
	}
	if secs > MaxSampleSeconds {
		return SampleInfo{}, &SampleError{ReasonTooLong}
	}
	return SampleInfo{Format: format, Seconds: secs, Bytes: size, PathOnFS: path}, nil
}

// probe recognizes the container and measures it.
func probe(b []byte) (SampleFormat, float64, error) {
	switch {
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WAVE":
		s, err := wavSeconds(b)
		return FormatWAV, s, err
	case len(b) >= 4 && string(b[0:4]) == "fLaC":
		s, err := flacSeconds(b)
		return FormatFLAC, s, err
	case len(b) >= 4 && string(b[0:4]) == "OggS":
		s, err := oggSeconds(b)
		return FormatOGG, s, err
	case len(b) >= 3 && string(b[0:3]) == "ID3", len(b) >= 2 && b[0] == 0xFF && b[1]&0xE0 == 0xE0:
		s, err := mp3Seconds(b)
		return FormatMP3, s, err
	}
	return SampleFormat{}, 0, &SampleError{ReasonUnsupportedType}
}

var errUnknown = &SampleError{ReasonUnknownDuration}

func wavSeconds(b []byte) (float64, error) {
	var byteRate uint32
	pos := 12
	for pos+8 <= len(b) {
		id := string(b[pos : pos+4])
		n := int64(binary.LittleEndian.Uint32(b[pos+4 : pos+8]))
		body := pos + 8
		switch id {
		case "fmt ":
			if body+12 > len(b) {
				return 0, errUnknown
			}
			byteRate = binary.LittleEndian.Uint32(b[body+8 : body+12])
		case "data":
			if byteRate == 0 {
				return 0, errUnknown
			}
			avail := int64(len(b) - body)
			if n > avail || n == 0xFFFFFFFF { // a streamed header: what is there is the data
				n = avail
			}
			return float64(n) / float64(byteRate), nil
		}
		next := int64(body) + n + n%2
		if next > int64(len(b)) {
			break
		}
		pos = int(next)
	}
	return 0, errUnknown
}

func flacSeconds(b []byte) (float64, error) {
	// STREAMINFO is always the first metadata block: 4 bytes of header, 34 bytes of body.
	if len(b) < 4+4+34 || b[4]&0x7F != 0 {
		return 0, errUnknown
	}
	si := b[8:]
	rate := uint32(si[10])<<12 | uint32(si[11])<<4 | uint32(si[12])>>4
	total := uint64(si[13]&0x0F)<<32 | uint64(binary.BigEndian.Uint32(si[14:18]))
	if rate == 0 || total == 0 {
		return 0, errUnknown
	}
	return float64(total) / float64(rate), nil
}

func oggSeconds(b []byte) (float64, error) {
	// The first page carries the identification header of the codec.
	if len(b) < 27 {
		return 0, errUnknown
	}
	nseg := int(b[26])
	start := 27 + nseg
	if start > len(b) {
		return 0, errUnknown
	}
	payload := b[start:]
	var rate, preSkip uint64
	switch {
	case len(payload) >= 16 && payload[0] == 1 && string(payload[1:7]) == "vorbis":
		rate = uint64(binary.LittleEndian.Uint32(payload[12:16]))
	case len(payload) >= 12 && string(payload[0:8]) == "OpusHead":
		rate = 48000
		preSkip = uint64(binary.LittleEndian.Uint16(payload[10:12]))
	default:
		return 0, errUnknown
	}
	// The granule position of the last page is the number of samples.
	last := bytes.LastIndex(b, []byte("OggS"))
	if rate == 0 || last < 0 || last+14 > len(b) {
		return 0, errUnknown
	}
	granule := binary.LittleEndian.Uint64(b[last+6 : last+14])
	if granule == 0 || granule == ^uint64(0) || granule <= preSkip {
		return 0, errUnknown
	}
	return float64(granule-preSkip) / float64(rate), nil
}

// MPEG audio layer III: bitrates (kbit/s) and sample rates by version.
var (
	mp3BitrateV1  = [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	mp3BitrateV2  = [16]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
	mp3RateV1     = [4]int{44100, 48000, 32000, 0}
	errNotLayer3  = errors.New("not layer III")
	mp3SyncWindow = 64 << 10
)

func mp3Seconds(b []byte) (float64, error) {
	pos := 0
	if len(b) >= 10 && string(b[0:3]) == "ID3" {
		size := int(b[6]&0x7F)<<21 | int(b[7]&0x7F)<<14 | int(b[8]&0x7F)<<7 | int(b[9]&0x7F)
		pos = 10 + size
		if b[5]&0x10 != 0 {
			pos += 10 // footer
		}
	}
	// Find the first frame header within a window after the tag.
	end := min(len(b)-4, pos+mp3SyncWindow)
	for ; pos >= 0 && pos < end; pos++ {
		if b[pos] == 0xFF && b[pos+1]&0xE0 == 0xE0 {
			if secs, err := mp3FromFrame(b, pos); err == nil {
				return secs, nil
			}
		}
	}
	return 0, errUnknown
}

func mp3FromFrame(b []byte, pos int) (float64, error) {
	h := b[pos : pos+4]
	version := (h[1] >> 3) & 3 // 3 = MPEG1, 2 = MPEG2, 0 = MPEG2.5
	layer := (h[1] >> 1) & 3   // 1 = layer III
	if version == 1 || layer != 1 {
		return 0, errNotLayer3
	}
	bi, ri := int(h[2]>>4), int((h[2]>>2)&3)
	bitrate := mp3BitrateV1[bi]
	rate := mp3RateV1[ri]
	samplesPerFrame := 1152
	if version != 3 {
		bitrate = mp3BitrateV2[bi]
		rate /= 2
		if version == 0 {
			rate /= 2
		}
		samplesPerFrame = 576
	}
	if bitrate == 0 || rate == 0 {
		return 0, errUnknown
	}
	mono := (h[3] >> 6) == 3
	// A Xing or Info header (the first frame of a VBR file) carries the frame count.
	side := 32
	switch {
	case version == 3 && mono:
		side = 17
	case version != 3 && mono:
		side = 9
	case version != 3:
		side = 17
	}
	x := pos + 4 + side
	if x+12 <= len(b) && (string(b[x:x+4]) == "Xing" || string(b[x:x+4]) == "Info") {
		flags := binary.BigEndian.Uint32(b[x+4 : x+8])
		if flags&1 != 0 {
			frames := binary.BigEndian.Uint32(b[x+8 : x+12])
			if frames > 0 {
				return float64(frames) * float64(samplesPerFrame) / float64(rate), nil
			}
		}
	}
	// Constant bitrate: the length follows from the size.
	return float64(len(b)-pos) * 8 / float64(bitrate*1000), nil
}

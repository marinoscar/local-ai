package music_test

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/music"
)

func TestInspectSample(t *testing.T) {
	for _, c := range []struct {
		name   string
		data   []byte
		format music.SampleFormat
		secs   float64
		reason string
	}{
		{"wav", wavBytes(3), music.FormatWAV, 3, ""},
		{"wav too long", wavBytes(60.5), music.SampleFormat{}, 0, music.ReasonTooLong},
		{"wav without data", wavBytes(1)[:36], music.SampleFormat{}, 0, music.ReasonUnknownDuration},
		{"flac", flacBytes(44100, 44100*12), music.FormatFLAC, 12, ""},
		{"flac too long", flacBytes(48000, 48000*61), music.SampleFormat{}, 0, music.ReasonTooLong},
		{"vorbis", vorbisOgg(44100, 44100*7), music.FormatOGG, 7, ""},
		{"opus", opusOgg(48000 * 9), music.FormatOGG, 9, ""},
		{"ogg of another codec", oggPage(100, []byte("speex-header-bytes")), music.SampleFormat{}, 0, music.ReasonUnknownDuration},
		{"mp3 cbr", mp3CBR(4), music.FormatMP3, 4, ""},
		{"mp3 vbr", mp3Xing(1000), music.FormatMP3, 1000 * 1152 / 44100.0, ""},
		{"mp3 too long", mp3CBR(70), music.SampleFormat{}, 0, music.ReasonTooLong},
		{"text", []byte("hello world, not audio"), music.SampleFormat{}, 0, music.ReasonUnsupportedType},
		{"empty", []byte{}, music.SampleFormat{}, 0, music.ReasonEmpty},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "s")
			if err := os.WriteFile(p, c.data, 0o600); err != nil {
				t.Fatal(err)
			}
			info, err := music.InspectSample(p)
			if c.reason != "" {
				var se *music.SampleError
				if !errors.As(err, &se) || se.Reason != c.reason {
					t.Fatalf("err = %v, want reason %s", err, c.reason)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if info.Format != c.format || math.Abs(info.Seconds-c.secs) > 0.05 {
				t.Fatalf("info = %+v, want %v %v", info, c.format, c.secs)
			}
		})
	}
}

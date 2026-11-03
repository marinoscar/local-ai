package piper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// Canonical names and limits of the exec contract (companion-services.md section 4.3.2;
// overview.md section 3.2 owns the paths).
const (
	// DefaultRoot is /opt/localai/piper: one directory per version and the "current" symlink.
	DefaultRoot = "/opt/localai/piper"
	// CurrentName is the symlink the agent runs piper through; Install repoints it atomically.
	CurrentName = "current"
	// BinPiper is the program inside a version directory.
	BinPiper = "piper"
	// EspeakData is the phoneme data directory inside a version directory.
	EspeakData = "espeak-ng-data"
	// DefaultWorkDir is the working directory of the child: the agent's state directory.
	DefaultWorkDir = "/var/lib/localai"

	// MaxChars is the longest text, in characters after cleanup, one request may speak.
	MaxChars = 2000
	// MaxRawBytes bounds the text before cleanup, so that cleaning stays cheap whatever arrives
	// (the request body limit is 1 MiB).
	MaxRawBytes = 64 << 10
	// MaxStderr is how much of the program's stderr is kept for LAI-SVC-023 (the first bytes).
	MaxStderr = 4 << 10
	// SentenceSilence is the pause between sentences, in seconds (the --sentence_silence argument).
	SentenceSilence = "0.25"

	// BaseTimeout and PerChar make the timeout of a synthesis: 10 s plus 50 ms per character,
	// at most MaxTimeout.
	BaseTimeout = 10 * time.Second
	PerChar     = 50 * time.Millisecond
	MaxTimeout  = 110 * time.Second

	// Resource limits of the program: 2 GiB of address space, 60 s of CPU, 64 open files.
	LimitAS     = 2 << 30
	LimitCPU    = 60
	LimitNOFILE = 64

	// maxConfigBytes bounds the read of a voice's .onnx.json (real ones are a few KiB).
	maxConfigBytes = 1 << 20
	// maxAudioSeconds bounds the audio of one synthesis: 2000 characters never make more than a few
	// minutes of speech, so a program that keeps writing is cut off.
	maxAudioSeconds = 20 * 60
)

// SynthTimeout is the timeout of a synthesis of chars characters.
func SynthTimeout(chars int) time.Duration {
	if chars < 0 {
		chars = 0
	}
	t := BaseTimeout + time.Duration(chars)*PerChar
	if t > MaxTimeout {
		return MaxTimeout
	}
	return t
}

// Failure reasons of a synthesis (SynthError.Reason). All but ReasonNotInstalled are LAI-SVC-023.
const (
	ReasonNotInstalled = "not_installed" // the program is missing or not executable: LAI-SVC-020
	ReasonStart        = "start_failed"
	ReasonExit         = "exit_status"
	ReasonTimeout      = "timeout"
	ReasonEmpty        = "empty_audio"
	ReasonTooLong      = "audio_too_long"
	ReasonBadConfig    = "bad_voice_config"
)

// SynthError is why a synthesis failed. It carries the doctor code (Code) and, for a program that
// ran, its exit status and the first 4 KiB of its stderr.
type SynthError struct {
	Reason   string
	ExitCode int
	Stderr   string
	Err      error
}

// Error implements error. The text never contains the text that was spoken.
func (e *SynthError) Error() string {
	s := "piper: " + e.Reason
	if e.Reason == ReasonExit {
		s += fmt.Sprintf(" %d", e.ExitCode)
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap returns the cause.
func (e *SynthError) Unwrap() error { return e.Err }

// Code is LAI-SVC-020 when the program is not installed and LAI-SVC-023 for every other failure.
func (e *SynthError) Code() string {
	if e.Reason == ReasonNotInstalled {
		return doctor.SVC020
	}
	return doctor.SVC023
}

// Runtime runs piper: the exec contract of section 4.3.2 in one place.
type Runtime struct {
	// Streamer starts the program; default execx.Exec{}.
	Streamer execx.Streamer
	// FS reads the voice config and looks at the binary; default config.OSFS{}.
	FS config.FS
	// Root is the install root; default DefaultRoot.
	Root string
	// WorkDir is the working directory of the child; default DefaultWorkDir.
	WorkDir string
	// Credential runs the child as the localai user. nil keeps the agent's own user (a development
	// machine, or an agent that already is localai).
	Credential *execx.Credential
}

func (rt Runtime) streamer() execx.Streamer {
	if rt.Streamer == nil {
		return execx.Exec{}
	}
	return rt.Streamer
}

func (rt Runtime) fs() config.FS {
	if rt.FS == nil {
		return config.OSFS{}
	}
	return rt.FS
}

func (rt Runtime) root() string {
	if rt.Root == "" {
		return DefaultRoot
	}
	return filepath.Clean(rt.Root)
}

// Current is <root>/current, the directory the program and its libraries are used from.
func (rt Runtime) Current() string { return filepath.Join(rt.root(), CurrentName) }

// Binary is <root>/current/piper.
func (rt Runtime) Binary() string { return filepath.Join(rt.Current(), BinPiper) }

// CheckBinary reports whether the program exists and is an executable regular file. The error is a
// *SynthError with ReasonNotInstalled (LAI-SVC-020).
func (rt Runtime) CheckBinary() error {
	fi, err := rt.fs().Stat(rt.Binary())
	switch {
	case err != nil:
		return &SynthError{Reason: ReasonNotInstalled, Err: err}
	case !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0:
		return &SynthError{Reason: ReasonNotInstalled, Err: fmt.Errorf("%s is not an executable file", rt.Binary())}
	}
	return nil
}

// LengthScaleArg is the value of --length_scale: piper.length_scale divided by speed (speed 1.5 is
// faster, so a smaller scale), rounded to three decimals.
func LengthScaleArg(lengthScale, speed float64) string {
	if speed <= 0 {
		speed = 1
	}
	v := float64(int64((lengthScale/speed)*1000+0.5)) / 1000
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// Cmd builds the command line of one synthesis. The argv is fixed apart from the two voice paths
// (which come from the catalog and the ark index, never from the request) and the length scale (a
// number). The text is NOT in it: it goes to stdin, followed by a newline, so no character of it
// can reach a shell, an option or a path.
func (rt Runtime) Cmd(v Voice, text string, lengthScale, speed float64) execx.StreamCmd {
	cur := rt.Current()
	wd := rt.WorkDir
	if wd == "" {
		wd = DefaultWorkDir
	}
	return execx.StreamCmd{
		Path: rt.Binary(),
		Args: []string{
			"--model", v.Model,
			"--config", v.Config,
			"--espeak_data", filepath.Join(cur, EspeakData),
			"--output_raw",
			"--length_scale", LengthScaleArg(lengthScale, speed),
			"--sentence_silence", SentenceSilence,
		},
		Env:        []string{"LD_LIBRARY_PATH=" + cur}, // nothing else: the environment is cleared
		Dir:        wd,
		Stdin:      strings.NewReader(text + "\n"),
		Credential: rt.Credential,
		Rlimits: []execx.Rlimit{
			{Resource: execx.RlimitAS, Limit: LimitAS},
			{Resource: execx.RlimitCPU, Limit: LimitCPU},
			{Resource: execx.RlimitNOFILE, Limit: LimitNOFILE},
		},
		Timeout:   SynthTimeout(len([]rune(text))),
		MaxStderr: MaxStderr,
	}
}

// SampleRate reads audio.sample_rate from the voice's .onnx.json and checks it is a rate a WAV
// header may carry.
func (rt Runtime) SampleRate(v Voice) (int, error) {
	b, err := readLimited(rt.fs(), v.Config, maxConfigBytes)
	if err != nil {
		return 0, &SynthError{Reason: ReasonBadConfig, Err: err}
	}
	var vc voiceConfig
	if err := json.Unmarshal(b, &vc); err != nil {
		return 0, &SynthError{Reason: ReasonBadConfig, Err: fmt.Errorf("%s is not valid JSON", filepath.Base(v.Config))}
	}
	if r := vc.Audio.SampleRate; r < MinSampleRate || r > MaxSampleRate {
		return 0, &SynthError{Reason: ReasonBadConfig, Err: fmt.Errorf("audio.sample_rate %d is outside %d..%d", r, MinSampleRate, MaxSampleRate)}
	}
	return vc.Audio.SampleRate, nil
}

func readLimited(fs config.FS, name string, limit int64) ([]byte, error) {
	fi, err := fs.Stat(name)
	if err != nil {
		return nil, err
	}
	if fi.Size() > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", filepath.Base(name), limit)
	}
	return fs.ReadFile(name)
}

// Start starts the program for text with the voice. The caller reads Process.Stdout to its end (or
// kills the process) and then calls Wait; Settle turns the two outcomes into the error of the
// request.
func (rt Runtime) Start(ctx context.Context, v Voice, text string, cfg Config, speed float64) (execx.Process, error) {
	if err := rt.CheckBinary(); err != nil {
		return nil, err
	}
	p, err := rt.streamer().Start(ctx, rt.Cmd(v, text, cfg.LengthScale, speed))
	if err != nil {
		return nil, &SynthError{Reason: ReasonStart, Err: err}
	}
	return p, nil
}

// Settle reads how a finished process ended and classifies it, given the number of audio bytes that
// came out. It returns nil for a clean run that produced audio, the context's error when the
// request ended first (the client left or the route deadline passed: not piper's fault), and a
// *SynthError otherwise.
func Settle(res execx.StreamResult, waitErr error, audioBytes int64) error {
	stderr := sanitize(res.Stderr)
	switch {
	case errors.Is(waitErr, execx.ErrTimeout):
		return &SynthError{Reason: ReasonTimeout, ExitCode: res.ExitCode, Stderr: stderr, Err: waitErr}
	case errors.Is(waitErr, context.Canceled), errors.Is(waitErr, context.DeadlineExceeded):
		return waitErr
	case waitErr != nil:
		return &SynthError{Reason: ReasonStart, ExitCode: res.ExitCode, Stderr: stderr, Err: waitErr}
	case res.ExitCode != 0:
		return &SynthError{Reason: ReasonExit, ExitCode: res.ExitCode, Stderr: stderr}
	case audioBytes < 2:
		return &SynthError{Reason: ReasonEmpty, Stderr: stderr}
	}
	return nil
}

// sanitize makes captured stderr safe to log and to put in an error envelope: valid UTF-8, no
// control characters but the newline.
func sanitize(b []byte) string {
	s := strings.ToValidUTF8(string(b), "")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || !unicode.IsControl(r) {
			return r
		}
		return ' '
	}, s)
	return strings.TrimSpace(s)
}

// Synthesize runs one synthesis to the end and returns the PCM (raw s16le mono) and its sample
// rate. It is for the deep check and the smoke test, where the audio is measured, not streamed;
// the PCM is bounded by maxAudioSeconds.
func (rt Runtime) Synthesize(ctx context.Context, v Voice, text string, cfg Config, speed float64) ([]byte, int, error) {
	rate, err := rt.SampleRate(v)
	if err != nil {
		return nil, 0, err
	}
	p, err := rt.Start(ctx, v, text, cfg, speed)
	if err != nil {
		return nil, 0, err
	}
	limit := int64(rate) * 2 * maxAudioSeconds
	pcm, rerr := io.ReadAll(io.LimitReader(p.Stdout(), limit+1))
	if int64(len(pcm)) > limit {
		p.Kill()
		_, _ = io.Copy(io.Discard, p.Stdout())
		_, _ = p.Wait()
		return nil, 0, &SynthError{Reason: ReasonTooLong}
	}
	if rerr != nil {
		p.Kill()
	}
	res, werr := p.Wait()
	if err := Settle(res, werr, int64(len(pcm))); err != nil {
		return nil, 0, err
	}
	return pcm[:len(pcm)&^1], rate, nil
}

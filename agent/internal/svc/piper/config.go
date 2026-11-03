package piper

import (
	"bytes"
	"fmt"
	"regexp"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// Defaults of the node.yaml piper block (companion-services.md section 3.1).
const (
	// DefaultVersion is the pinned release of the rhasspy/piper C++ binary.
	DefaultVersion = "2023.11.14-2"
	// DefaultLengthScale is piper.length_scale: above 1 is slower and clearer.
	DefaultLengthScale = 1.05
	// AutoVoice is the value of piper.default_voice.<lang> that means "the first voice of that
	// language in the profile".
	AutoVoice = "auto"
)

// Bounds of piper.length_scale: outside them the speech is unintelligible.
const (
	MinLengthScale = 0.3
	MaxLengthScale = 3.0
)

// DefaultVoices is piper.default_voice.
type DefaultVoices struct {
	EN string `yaml:"en"`
	ES string `yaml:"es"`
}

// Config is the piper block of node.yaml.
type Config struct {
	Enabled      bool          `yaml:"enabled"`
	Version      string        `yaml:"version"`
	LengthScale  float64       `yaml:"length_scale"`
	DefaultVoice DefaultVoices `yaml:"default_voice"`
}

// DefaultConfig is the block as written when node.yaml has none.
func DefaultConfig() Config {
	return Config{
		Enabled: true, Version: DefaultVersion, LengthScale: DefaultLengthScale,
		DefaultVoice: DefaultVoices{EN: AutoVoice, ES: AutoVoice},
	}
}

// DefaultVoiceFor returns piper.default_voice for a language ("auto" for one the block does not
// name).
func (c Config) DefaultVoiceFor(lang string) string {
	switch lang {
	case LangEN:
		return c.DefaultVoice.EN
	case LangES:
		return c.DefaultVoice.ES
	}
	return AutoVoice
}

// ParseBlock reads the piper block of a node.yaml (a missing or empty block is DefaultConfig).
// Keys the block does not have keep their defaults; an unknown key is ignored here and reported by
// BlockValidator. A block that does not parse at all is an error.
func ParseBlock(raw config.RawBlock) (Config, error) {
	c := DefaultConfig()
	if len(raw) == 0 {
		return c, nil
	}
	if err := raw.Decode(&c); err != nil {
		return DefaultConfig(), fmt.Errorf("piper: %w", err)
	}
	return c, nil
}

// FromConfig reads the piper block of a loaded node.yaml.
func FromConfig(cfg config.Config) Config {
	c, err := ParseBlock(cfg.Blocks["piper"])
	if err != nil {
		return DefaultConfig() // the validator already refused such a file
	}
	return c
}

var versionRe = regexp.MustCompile(`^[0-9][0-9A-Za-z.+_-]{0,63}$`)

// BlockValidator checks the piper block when node.yaml loads (config.Hooks.Blocks["piper"]). A bad
// value is an error: the last valid node.yaml stays in use.
type BlockValidator struct{}

// ValidateBlock implements config.BlockValidator.
func (BlockValidator) ValidateBlock(raw config.RawBlock) config.Problems {
	var probs config.Problems
	bad := func(path, msg string) {
		probs = append(probs, config.Problem{Path: path, Message: msg, Severity: config.SeverityError})
	}
	c := DefaultConfig()
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil // an empty block is the defaults
	}
	if err := raw.DecodeStrict(&c); err != nil {
		bad("", err.Error())
		return probs
	}
	if !versionRe.MatchString(c.Version) {
		bad("version", fmt.Sprintf("%q is not a release number such as %s", c.Version, DefaultVersion))
	}
	if c.LengthScale < MinLengthScale || c.LengthScale > MaxLengthScale {
		bad("length_scale", fmt.Sprintf("%v is outside %v..%v", c.LengthScale, MinLengthScale, MaxLengthScale))
	}
	for _, d := range []struct{ lang, id string }{{LangEN, c.DefaultVoice.EN}, {LangES, c.DefaultVoice.ES}} {
		if d.id != AutoVoice && !manifest.SafeName(d.id) {
			bad("default_voice."+d.lang, fmt.Sprintf("%q is neither auto nor a catalog voice id", d.id))
		}
	}
	return probs
}

var _ config.BlockValidator = BlockValidator{}

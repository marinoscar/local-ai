// Package logx sets up the agent's structured logging (agent.md section 4.11): a slog JSON
// handler without a time attribute (journald adds it), a runtime-changeable level, and the
// secret-scrubbing wrapper (scrub.go) that every log line passes through.
//
// The package uses the standard library only and never touches the network, so it works fully
// offline.
package logx

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// EnvLevel is the environment variable that sets the initial log level, until node.yaml
// (logging.level) has been read (agent.md section 4.1 step 2).
const EnvLevel = "LOCALAI_LOG_LEVEL"

// DefaultLevel is used when EnvLevel is unset, empty or invalid.
const DefaultLevel = slog.LevelInfo

// New returns a logger that writes one JSON object per line to w, with the time attribute removed
// and every message and string attribute scrubbed of secrets (Scrub).
//
// The initial level comes from LOCALAI_LOG_LEVEL (debug, info, warn or error), default info, and is
// stored in level, which the caller keeps to change the level at runtime (SetLevel). A nil level
// is allowed when the level never changes. An invalid environment value falls back to info and
// logs a warning.
func New(w io.Writer, level *slog.LevelVar) *slog.Logger {
	if level == nil {
		level = new(slog.LevelVar)
	}
	initial, envErr := levelFromEnv()
	level.Set(initial)
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level, ReplaceAttr: dropTime})
	l := slog.New(Scrub(h))
	if envErr != nil {
		l.Warn("invalid "+EnvLevel+"; using the default level", "error", envErr.Error(), "level_used", DefaultLevel.String())
	}
	return l
}

// ParseLevel parses a level name as written in node.yaml and the environment: debug, info, warn
// or error, in any letter case, surrounding spaces ignored.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return DefaultLevel, fmt.Errorf("unknown log level %q (want debug, info, warn or error)", s)
}

// SetLevel changes the level of a logger made by New (the `logging.level` hot reload). On an
// invalid name it returns an error and leaves the level unchanged.
func SetLevel(level *slog.LevelVar, name string) error {
	l, err := ParseLevel(name)
	if err != nil {
		return err
	}
	level.Set(l)
	return nil
}

func levelFromEnv() (slog.Level, error) {
	v, ok := os.LookupEnv(EnvLevel)
	if !ok || strings.TrimSpace(v) == "" {
		return DefaultLevel, nil
	}
	l, err := ParseLevel(v)
	if err != nil {
		return DefaultLevel, err
	}
	return l, nil
}

// dropTime removes the top-level time attribute: journald stamps every line itself.
func dropTime(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey {
		return slog.Attr{}
	}
	return a
}

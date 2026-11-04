package setup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

func init() { register("api-key", apiKeyStep) }

const (
	apiKeyPath    = "/etc/localai/api-key"
	legacyKeyPath = "/etc/llama-qwen/api-key"
)

var apiKeyRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// MaskKey is the only form of a key that may be printed: the first and last four characters.
func MaskKey(k string) string {
	if len(k) < 12 {
		return "…"
	}
	return k[:4] + "…" + k[len(k)-4:]
}

// apiKeyStep is `aiagent setup api-key` (installer.md P10): keep /etc/localai/api-key when it exists, reuse
// the legacy llama-qwen key when only that exists, else generate 32 random bytes as 64 lowercase hex. It
// prints the masked key only.
func apiKeyStep(_ context.Context, e *Env, args []string) int {
	fs := flag.NewFlagSet("api-key", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var dry bool
	fs.BoolVar(&dry, "dry-run", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(e.Stderr, "usage: aiagent setup api-key [--dry-run]")
		return ExitUsage
	}
	if !needRoot(e, dry, "api-key") {
		return ExitNotRoot
	}
	path := filepath.Join(e.Root, apiKeyPath)
	legacy := filepath.Join(e.Root, legacyKeyPath)

	if data, err := os.ReadFile(path); err == nil {
		key := trimNL(data)
		if !apiKeyRe.MatchString(key) {
			fmt.Fprintf(e.Stderr, "aiagent setup api-key: %s exists but is not 64 lowercase hex characters. It was left alone; run \"sudo aictl key rotate\" to replace it.\n", path)
			return ExitFailed
		}
		if !dry {
			if err := os.Chmod(path, 0o600); err != nil {
				fmt.Fprintf(e.Stderr, "aiagent setup api-key: cannot set the mode of %s: %v\n", path, err)
				return ExitFailed
			}
		}
		e.say("API key kept: %s", MaskKey(key))
		e.say("`aictl key show` prints the full key.")
		return ExitOK
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(e.Stderr, "aiagent setup api-key: cannot read %s: %v\n", path, err)
		return ExitFailed
	}

	key := ""
	source := "generated"
	if data, err := os.ReadFile(legacy); err == nil && apiKeyRe.MatchString(trimNL(data)) {
		key, source = trimNL(data), "reused from the old llama-qwen service"
	}
	if key == "" {
		if dry {
			e.say("[dry-run] would generate a new API key in %s (root, mode 0600)", path)
			return ExitOK
		}
		r := e.Random
		if r == nil {
			r = rand.Reader
		}
		buf := make([]byte, 32)
		if _, err := io.ReadFull(r, buf); err != nil {
			fmt.Fprintf(e.Stderr, "aiagent setup api-key: no random data available: %v\n", err)
			return ExitFailed
		}
		key = hex.EncodeToString(buf)
	} else if dry {
		e.say("[dry-run] would copy the old llama-qwen key to %s (root, mode 0600)", path)
		return ExitOK
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintf(e.Stderr, "aiagent setup api-key: cannot create %s: %v\n", filepath.Dir(path), err)
		return ExitFailed
	}
	if err := config.WriteFileAtomic(path, []byte(key+"\n"), 0o600, -1, -1); err != nil {
		fmt.Fprintf(e.Stderr, "aiagent setup api-key: cannot write %s: %v\n", path, err)
		return ExitFailed
	}
	e.say("API key %s: %s", source, MaskKey(key))
	e.say("`aictl key show` prints the full key.")
	return ExitOK
}

func trimNL(b []byte) string {
	s := string(b)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

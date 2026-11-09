package netx

import (
	"context"
	"strings"
)

type ctxKey int

const (
	callerKey ctxKey = iota
	probeKey
)

const maxCallerLen = 48

// cleanCaller keeps a caller name to letters, digits and . _ - : so it is safe in a log line and in
// the drill's "; "-joined list.
func cleanCaller(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxCallerLen {
		s = s[:maxCallerLen]
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-', r == ':':
			return r
		}
		return '_'
	}, s)
}

// WithCaller names the component that makes the requests of ctx, for the drill's report (for example
// "cloud" or "sync.resolver"). An empty name leaves ctx as it is; a context that already names a caller
// is overridden by the nearer one.
func WithCaller(ctx context.Context, caller string) context.Context {
	if c := cleanCaller(caller); c != "" {
		return context.WithValue(ctx, callerKey, c)
	}
	return ctx
}

// CallerFrom returns the caller WithCaller set, or "".
func CallerFrom(ctx context.Context) string {
	s, _ := ctx.Value(callerKey).(string)
	return s
}

// WithProbe marks the dials of ctx as a self-test: the fence still refuses them, but does not count
// them. The drill's own fence check dials the internet on purpose and must not count as a feature that
// needs it.
func WithProbe(ctx context.Context) context.Context { return context.WithValue(ctx, probeKey, true) }

func isProbe(ctx context.Context) bool {
	b, _ := ctx.Value(probeKey).(bool)
	return b
}

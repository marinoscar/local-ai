package services

import (
	"log/slog"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// DefaultPollEvery is how often the systemctl Manager polls for state changes (agent.md section 4.3.2).
const DefaultPollEvery = 5 * time.Second

// DefaultRedialEvery is how long NewAuto waits between attempts to get the D-Bus connection back
// while it is down and systemctl does the work.
const DefaultRedialEvery = 30 * time.Second

// Option configures a Manager implementation.
type Option func(*options)

type options struct {
	jobTimeout  time.Duration
	callTimeout time.Duration
	pollEvery   time.Duration
	clock       config.Clock
	path        string
	log         *slog.Logger
	redialEvery time.Duration
}

func newOptions(opts []Option) options {
	o := options{jobTimeout: JobTimeout, callTimeout: CallTimeout, pollEvery: DefaultPollEvery, redialEvery: DefaultRedialEvery, clock: config.RealClock{}}
	for _, f := range opts {
		f(&o)
	}
	return o
}

// WithTimeouts overrides the job timeout (start, stop, restart) and the call timeout (everything
// else). A zero value keeps the default.
func WithTimeouts(job, call time.Duration) Option {
	return func(o *options) {
		if job > 0 {
			o.jobTimeout = job
		}
		if call > 0 {
			o.callTimeout = call
		}
	}
}

// WithPollEvery overrides the polling interval of a systemctl subscription.
func WithPollEvery(d time.Duration) Option {
	return func(o *options) {
		if d > 0 {
			o.pollEvery = d
		}
	}
}

// WithClock replaces the wall clock of the polling loop (tests).
func WithClock(c config.Clock) Option { return func(o *options) { o.clock = c } }

// WithSystemctlPath sets the absolute path of the systemctl binary. The default is
// /usr/bin/systemctl, or what execx.Lookup finds in the fixed PATH.
func WithSystemctlPath(p string) Option { return func(o *options) { o.path = p } }

// WithLogger sets the logger of a Manager (the component=services attribute is added).
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.log = l } }

// WithRedialEvery sets how long NewAuto waits between attempts to re-dial D-Bus after a loss.
func WithRedialEvery(d time.Duration) Option {
	return func(o *options) {
		if d > 0 {
			o.redialEvery = d
		}
	}
}

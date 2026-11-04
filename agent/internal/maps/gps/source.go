package gps

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Mode is the maps.gps setting of node.yaml (companion-services.md section 3.1).
type Mode string

// The modes. nmea:<device> is ModeNMEA with Config.Device set.
const (
	ModeAuto Mode = "auto"
	ModeOff  Mode = "off"
	ModeGPSD Mode = "gpsd"
	ModeNMEA Mode = "nmea"
)

// Config is the parsed maps.gps value.
type Config struct {
	Mode Mode
	// Device is the tty of ModeNMEA ("/dev/ttyACM0").
	Device string
}

// ParseConfig parses auto | off | gpsd | nmea:<device>. An empty value is auto. The device must be a
// serial tty under /dev.
func ParseConfig(s string) (Config, error) {
	switch s = strings.TrimSpace(s); {
	case s == "" || s == "auto":
		return Config{Mode: ModeAuto}, nil
	case s == "off":
		return Config{Mode: ModeOff}, nil
	case s == "gpsd":
		return Config{Mode: ModeGPSD}, nil
	case strings.HasPrefix(s, "nmea:"):
		dev := strings.TrimPrefix(s, "nmea:")
		if !ValidDevicePath(dev) {
			return Config{}, fmt.Errorf("maps.gps: %q is not a serial device under /dev (ttyACM, ttyUSB, ttyS, ttyAMA)", dev)
		}
		return Config{Mode: ModeNMEA, Device: dev}, nil
	}
	return Config{}, fmt.Errorf("maps.gps: %q is not auto, off, gpsd or nmea:<device>", s)
}

// Timing rules (companion-services.md section 4.4.5).
const (
	// StaleAfter: a fix older than this is reported as "stale".
	StaleAfter = 60 * time.Second
	// SilentAfter: a configured GPS that sent nothing for this long raises LAI-SVC-033.
	SilentAfter = 2 * time.Minute
	// RescanEvery is how often the sysfs scan and the config check run.
	RescanEvery = 30 * time.Second
)

// Tracker holds the last fix and when a source became active. It is safe for concurrent use.
type Tracker struct {
	clock func() time.Time

	mu      sync.Mutex
	last    Fix
	has     bool
	armedAt time.Time
	armed   bool
}

// NewTracker returns a Tracker on clock (time.Now when nil).
func NewTracker(clock func() time.Time) *Tracker {
	if clock == nil {
		clock = time.Now
	}
	return &Tracker{clock: clock}
}

// Update records a fix.
func (t *Tracker) Update(f Fix) {
	t.mu.Lock()
	t.last, t.has = f, true
	t.mu.Unlock()
}

// Arm records that a source is active (gpsd connected, a device open). The silence timer of a
// source that never sent anything counts from the first Arm. Explicitly configured sources are
// armed by the manager as soon as it starts.
func (t *Tracker) Arm() {
	t.mu.Lock()
	if !t.armed {
		t.armed, t.armedAt = true, t.clock()
	}
	t.mu.Unlock()
}

// Latest returns the last fix and whether there was one.
func (t *Tracker) Latest() (Fix, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.last, t.has
}

// FixState is the "fix" value of GET /maps/whereami for a GPS position: "2d", "3d" or "stale".
func (t *Tracker) FixState(f Fix) string {
	if t.clock().Sub(f.At) > StaleAfter {
		return "stale"
	}
	if f.Mode >= 3 {
		return "3d"
	}
	return "2d"
}

// Silent reports whether a GPS is configured and has sent nothing for SilentAfter. In auto mode a GPS
// counts as configured once a source was active (gpsd connected, or a recognised receiver opened):
// a laptop with no receiver never raises LAI-SVC-033.
func (t *Tracker) Silent(c Config) bool {
	if c.Mode == ModeOff {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.armed {
		return false
	}
	ref := t.armedAt
	if t.has && t.last.At.After(ref) {
		ref = t.last.At
	}
	return t.clock().Sub(ref) > SilentAfter
}

// Manager picks the source per maps.gps and feeds the Tracker: gpsd first, then NMEA serial. It
// reconnects to gpsd with backoff (5 s to 60 s) and reopens the serial device when it reappears (the
// sysfs scan runs every 30 s).
type Manager struct {
	Config  func() Config
	Tracker *Tracker
	// GPSD is the gpsd client; its OnFix and OnState are set by Run.
	GPSD *GPSD
	// Detect lists the allowlisted receivers (default: Detect(SysClassTTY)).
	Detect func() []Device
	// Open opens a serial device (default OpenSerial).
	Open  func(Device) (io.ReadCloser, error)
	Sleep Sleeper
	Clock func() time.Time
}

// Run feeds the Tracker until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	if m.Sleep == nil {
		m.Sleep = RealSleep
	}
	if m.Clock == nil {
		m.Clock = time.Now
	}
	if m.Detect == nil {
		m.Detect = func() []Device { return Detect(SysClassTTY) }
	}
	if m.Open == nil {
		m.Open = OpenSerial
	}
	g := m.GPSD
	if g == nil {
		g = &GPSD{}
	}
	g.OnFix = m.Tracker.Update
	g.OnState = func(up bool) {
		if up {
			m.Tracker.Arm()
		}
	}
	if g.Clock == nil {
		g.Clock = m.Clock
	}
	backoff := time.Duration(0)
	for ctx.Err() == nil {
		cfg := m.Config()
		// The source ends when the setting changes, so a live reload of node.yaml takes effect.
		cctx, cancel := context.WithCancel(ctx)
		go m.watchConfig(cctx, cancel, cfg)
		progressed := m.once(cctx, cfg, g)
		cancel()
		if progressed {
			backoff = 0
		}
		backoff = NextBackoff(backoff)
		wait := backoff
		if cfg.Mode == ModeOff || (!progressed && cfg.Mode == ModeAuto) {
			wait = RescanEvery
		}
		if !m.Sleep(ctx, wait) {
			return
		}
	}
}

func (m *Manager) watchConfig(ctx context.Context, cancel context.CancelFunc, start Config) {
	for m.Sleep(ctx, RescanEvery) {
		if m.Config() != start {
			cancel()
			return
		}
	}
}

// once runs the highest-priority source that works and reports whether it delivered data.
func (m *Manager) once(ctx context.Context, cfg Config, g *GPSD) bool {
	switch cfg.Mode {
	case ModeOff:
		return false
	case ModeGPSD:
		m.Tracker.Arm()
		_, got := g.Once(ctx)
		return got
	case ModeNMEA:
		m.Tracker.Arm()
		return m.serial(ctx, DeviceForPath(cfg.Device))
	}
	// auto: gpsd if it accepts connections, else the first recognised receiver.
	if dialed, got := g.Once(ctx); dialed {
		return got
	}
	devs := m.Detect()
	if len(devs) == 0 {
		return false
	}
	return m.serial(ctx, devs[0])
}

// serial reads one device until it fails or ctx ends and reports whether any fix arrived.
func (m *Manager) serial(ctx context.Context, d Device) bool {
	rc, err := m.Open(d)
	if err != nil {
		return false
	}
	m.Tracker.Arm()
	stop := context.AfterFunc(ctx, func() { _ = rc.Close() })
	defer stop()
	defer rc.Close()
	return ReadNMEA(rc, m.Tracker.Update, m.Clock)
}

// ReadNMEA reads sentences from r until it ends, passing every fix to sink; it reports whether any
// fix was found.
func ReadNMEA(r io.Reader, sink func(Fix), clock func() time.Time) (got bool) {
	var n NMEA
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1024), 4096)
	for sc.Scan() {
		if f, ok := n.Feed(sc.Text(), clock()); ok {
			got = true
			sink(f)
		}
	}
	return got
}

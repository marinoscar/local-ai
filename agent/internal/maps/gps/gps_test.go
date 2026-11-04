package gps

import (
	"bufio"
	"context"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 2, 14, 5, 11, 0, time.UTC)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-4 }

func feedFile(t *testing.T, name string) (fixes []Fix) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ReadNMEA(f, func(x Fix) { fixes = append(fixes, x) }, func() time.Time { return t0 })
	return fixes
}

func TestNMEAFixtures(t *testing.T) {
	tests := []struct {
		file string
		want int
		last Fix
	}{
		{"valid.nmea", 2, Fix{Lat: 9.93241, Lon: -84.07912, Mode: 3, AccuracyM: 8}},
		{"nofix.nmea", 0, Fix{}},
		{"badchecksum.nmea", 0, Fix{}},
		{"mixed.nmea", 2, Fix{Lat: 9.93241, Lon: -84.07912, Mode: 3, AccuracyM: 8}},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			got := feedFile(t, tc.file)
			if len(got) != tc.want {
				t.Fatalf("%d fixes, want %d: %+v", len(got), tc.want, got)
			}
			if tc.want == 0 {
				return
			}
			l := got[len(got)-1]
			if !near(l.Lat, tc.last.Lat) || !near(l.Lon, tc.last.Lon) || l.Mode != tc.last.Mode || l.AccuracyM != tc.last.AccuracyM {
				t.Fatalf("last %+v, want %+v", l, tc.last)
			}
		})
	}
}

func TestNMEARMCAloneIs2D(t *testing.T) {
	var n NMEA
	f, ok := n.Feed("$GPRMC,140511.00,A,0955.9446,N,08404.7472,W,0.1,0.0,021126,,,A*43\r\n", t0)
	if !ok || f.Mode != 2 || f.AccuracyM != 0 || !f.At.Equal(t0) {
		t.Fatalf("%+v %v", f, ok)
	}
}

func TestNMEARejects(t *testing.T) {
	var n NMEA
	for name, line := range map[string]string{
		"empty":           "",
		"no dollar":       "GPRMC,140511.00,A,0955.9446,N,08404.7472,W,0.1,0.0,021126,,,A*43",
		"no checksum":     "$GPRMC,140511.00,A,0955.9446,N,08404.7472,W,0.1,0.0,021126,,,A",
		"wrong checksum":  "$GPRMC,140511.00,A,0955.9446,N,08404.7472,W,0.1,0.0,021126,,,A*44",
		"non hex":         "$GPRMC,140511.00,A,0955.9446,N,08404.7472,W,0.1,0.0,021126,,,A*ZZ",
		"other sentence":  "$GPGSV,3,1,11,01,40,083,46,02,17,308,41,12,07,344,39,14,22,228,45*7E",
		"truncated":       "$GPRMC,1405",
		"not nmea at all": "hello world",
	} {
		if f, ok := n.Feed(line, t0); ok {
			t.Errorf("%s accepted: %+v", name, f)
		}
	}
}

func TestParseCoordEdges(t *testing.T) {
	if _, ok := parseCoord("0960.0000", "N", 2); ok {
		t.Error("minutes 60 accepted")
	}
	if _, ok := parseCoord("0955.9446", "X", 2); ok {
		t.Error("bad hemisphere accepted")
	}
	if v, ok := parseCoord("0955.9446", "S", 2); !ok || !near(v, -9.93241) {
		t.Errorf("%v %v", v, ok)
	}
}

func TestParseTPV(t *testing.T) {
	tests := []struct {
		name string
		line string
		ok   bool
		mode int
		acc  float64
	}{
		{"3d", `{"class":"TPV","device":"/dev/ttyACM0","mode":3,"lat":9.93241,"lon":-84.07912,"epx":4.2,"epy":7.6}`, true, 3, 8},
		{"2d with eph", `{"class":"TPV","mode":2,"lat":9.5,"lon":-84.5,"eph":12.4}`, true, 2, 12},
		{"mode 1 no fix", `{"class":"TPV","mode":1}`, false, 0, 0},
		{"mode 0", `{"class":"TPV","mode":0,"lat":9.5,"lon":-84.5}`, false, 0, 0},
		{"mode 2 without position", `{"class":"TPV","mode":2}`, false, 0, 0},
		{"sky report", `{"class":"SKY","mode":3,"lat":1,"lon":1}`, false, 0, 0},
		{"out of range", `{"class":"TPV","mode":3,"lat":99,"lon":1}`, false, 0, 0},
		{"garbage", `not json`, false, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, ok := ParseTPV([]byte(tc.line), t0)
			if ok != tc.ok {
				t.Fatalf("ok=%v", ok)
			}
			if ok && (f.Mode != tc.mode || f.AccuracyM != tc.acc || !f.At.Equal(t0)) {
				t.Fatalf("%+v", f)
			}
		})
	}
}

func TestGPSDClient(t *testing.T) {
	client, server := net.Pipe()
	var mu sync.Mutex
	var fixes []Fix
	var states []bool
	g := &GPSD{
		Dial:    func(context.Context, string) (net.Conn, error) { return client, nil },
		Clock:   func() time.Time { return t0 },
		OnFix:   func(f Fix) { mu.Lock(); fixes = append(fixes, f); mu.Unlock() },
		OnState: func(up bool) { mu.Lock(); states = append(states, up); mu.Unlock() },
	}
	done := make(chan [2]bool)
	go func() { d, g2 := g.Once(context.Background()); done <- [2]bool{d, g2} }()

	line, err := bufio.NewReader(server).ReadString('\n')
	if err != nil || line != watchCommand {
		t.Fatalf("watch command %q %v", line, err)
	}
	io.WriteString(server, `{"class":"VERSION","release":"3.25"}`+"\n")
	io.WriteString(server, `{"class":"TPV","mode":1}`+"\n")
	io.WriteString(server, `{"class":"TPV","mode":3,"lat":9.93241,"lon":-84.07912,"eph":8}`+"\n")
	io.WriteString(server, `{"class":"TPV","mode":1}`+"\n") // a lost fix does not erase the last good one
	server.Close()
	r := <-done
	if !r[0] || !r[1] {
		t.Fatalf("dialed/got = %v", r)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(fixes) != 1 || fixes[0].Mode != 3 || len(states) != 2 || !states[0] || states[1] {
		t.Fatalf("fixes %+v states %v", fixes, states)
	}
}

func TestGPSDNotRunning(t *testing.T) {
	g := &GPSD{Dial: func(context.Context, string) (net.Conn, error) { return nil, io.ErrClosedPipe }}
	if dialed, got := g.Once(context.Background()); dialed || got {
		t.Fatal("dialed a dead gpsd")
	}
}

func TestGPSDRunBacksOff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var waits []time.Duration
	g := &GPSD{
		Dial: func(context.Context, string) (net.Conn, error) { return nil, io.ErrClosedPipe },
		Sleep: func(_ context.Context, d time.Duration) bool {
			waits = append(waits, d)
			if len(waits) == 6 {
				cancel()
				return false
			}
			return true
		},
	}
	g.Run(ctx)
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 60 * time.Second, 60 * time.Second}
	for i := range want {
		if waits[i] != want[i] {
			t.Fatalf("waits %v, want %v", waits, want)
		}
	}
}

// sysTree builds a fake /sys/class/tty with USB devices behind symlinks, the way the kernel lays it out.
func sysTree(t *testing.T, devs map[string][2]string) string {
	t.Helper()
	root := t.TempDir()
	cls := filepath.Join(root, "class", "tty")
	if err := os.MkdirAll(cls, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, id := range devs {
		usb := filepath.Join(root, "devices", "pci0", "usb1", "1-"+name)
		tty := filepath.Join(usb, "1-"+name+":1.0", "tty", name)
		if err := os.MkdirAll(tty, 0o755); err != nil {
			t.Fatal(err)
		}
		if id[0] != "" {
			os.WriteFile(filepath.Join(usb, "idVendor"), []byte(id[0]+"\n"), 0o644)
			os.WriteFile(filepath.Join(usb, "product"), []byte(id[1]+"\n"), 0o644)
		}
		if err := os.Symlink(tty, filepath.Join(cls, name)); err != nil {
			t.Skip(err)
		}
	}
	return cls
}

func TestDetect(t *testing.T) {
	cls := sysTree(t, map[string][2]string{
		"ttyACM0": {"1546", "u-blox 7 - GPS/GNSS Receiver"},
		"ttyACM1": {"1546", "Something else"},         // u-blox vendor, any product
		"ttyUSB0": {"067b", "USB-Serial GNSS Module"}, // product string
		"ttyUSB1": {"10c4", "CP2102 USB to UART Bridge"},
		"ttyACM2": {"2341", "Arduino Uno"},
		"ttyUSB2": {"0403", "FT232R USB UART"},
		"ttyS0":   {"1546", "GPS"}, // not ACM/USB: ignored by name
	})
	// A tty with no USB parent and an unrelated name.
	os.Symlink(t.TempDir(), filepath.Join(cls, "ttyUSB9"))
	os.Symlink(t.TempDir(), filepath.Join(cls, "tty0"))

	got := Detect(cls)
	var paths []string
	for _, d := range got {
		paths = append(paths, d.Path)
	}
	want := []string{"/dev/ttyACM0", "/dev/ttyACM1", "/dev/ttyUSB0"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", paths, want)
	}
	for _, d := range got {
		if d.USB != strings.Contains(d.Path, "ttyUSB") {
			t.Errorf("%+v", d)
		}
	}
	if Detect(filepath.Join(cls, "missing")) != nil {
		t.Error("missing sysfs should find nothing")
	}
}

func TestParseConfig(t *testing.T) {
	tests := []struct {
		in   string
		want Config
		err  bool
	}{
		{"", Config{Mode: ModeAuto}, false},
		{"auto", Config{Mode: ModeAuto}, false},
		{"off", Config{Mode: ModeOff}, false},
		{"gpsd", Config{Mode: ModeGPSD}, false},
		{"nmea:/dev/ttyACM0", Config{Mode: ModeNMEA, Device: "/dev/ttyACM0"}, false},
		{"nmea:/dev/ttyUSB3", Config{Mode: ModeNMEA, Device: "/dev/ttyUSB3"}, false},
		{"nmea:/dev/sda", Config{}, true},
		{"nmea:/etc/passwd", Config{}, true},
		{"nmea:", Config{}, true},
		{"bluetooth", Config{}, true},
	}
	for _, tc := range tests {
		got, err := ParseConfig(tc.in)
		if (err != nil) != tc.err || got != tc.want {
			t.Errorf("%q: %+v %v", tc.in, got, err)
		}
	}
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func TestTrackerStaleAndSilent(t *testing.T) {
	c := &clock{now: t0}
	tr := NewTracker(c.Now)
	auto := Config{Mode: ModeAuto}

	if tr.Silent(auto) {
		t.Fatal("silent with no source in auto mode")
	}
	tr.Arm()
	tr.Update(Fix{Lat: 9.9, Lon: -84.0, Mode: 3, At: c.now})
	f, _ := tr.Latest()
	for _, tc := range []struct {
		age    time.Duration
		fix    string
		silent bool
	}{
		{0, "3d", false},
		{60 * time.Second, "3d", false},
		{61 * time.Second, "stale", false},
		{2 * time.Minute, "stale", false},
		{2*time.Minute + time.Second, "stale", true},
	} {
		c.now = t0.Add(tc.age)
		if got := tr.FixState(f); got != tc.fix {
			t.Errorf("age %v: fix %s, want %s", tc.age, got, tc.fix)
		}
		if got := tr.Silent(auto); got != tc.silent {
			t.Errorf("age %v: silent %v, want %v", tc.age, got, tc.silent)
		}
		if tr.Silent(Config{Mode: ModeOff}) {
			t.Error("off is never silent")
		}
	}
	if got := tr.FixState(Fix{Mode: 2, At: c.now}); got != "2d" {
		t.Errorf("2d: %s", got)
	}

	// A source armed that never sends anything goes silent two minutes after it was armed.
	c.now = t0
	tr2 := NewTracker(c.Now)
	tr2.Arm()
	c.now = t0.Add(SilentAfter)
	if tr2.Silent(Config{Mode: ModeGPSD}) {
		t.Error("silent too early")
	}
	c.now = t0.Add(SilentAfter + time.Second)
	if !tr2.Silent(Config{Mode: ModeGPSD}) {
		t.Error("not silent after 2 minutes")
	}
}

// fakePort feeds NMEA text and records which devices were opened.
type fakeOpen struct {
	mu     sync.Mutex
	opened []string
	data   string
}

func (f *fakeOpen) Open(d Device) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, d.Path)
	return io.NopCloser(strings.NewReader(f.data)), nil
}

func validData(t *testing.T) string {
	b, err := os.ReadFile("testdata/valid.nmea")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func deadGPSD() *GPSD {
	return &GPSD{Dial: func(context.Context, string) (net.Conn, error) { return nil, io.ErrClosedPipe }}
}

func TestManagerSourceSelection(t *testing.T) {
	tests := []struct {
		name       string
		cfg        Config
		gpsd       bool
		detected   []Device
		wantOpened []string
		wantFix    bool
		wantArmed  bool
	}{
		{"off opens nothing", Config{Mode: ModeOff}, true, []Device{{Path: "/dev/ttyACM0"}}, nil, false, false},
		{"auto prefers gpsd", Config{Mode: ModeAuto}, true, []Device{{Path: "/dev/ttyACM0"}}, nil, true, true},
		{"auto falls back to the first recognised receiver", Config{Mode: ModeAuto}, false,
			[]Device{{Path: "/dev/ttyACM0"}, {Path: "/dev/ttyUSB0", USB: true}}, []string{"/dev/ttyACM0"}, true, true},
		{"auto with no receiver does nothing", Config{Mode: ModeAuto}, false, nil, nil, false, false},
		{"gpsd mode never opens serial", Config{Mode: ModeGPSD}, false, []Device{{Path: "/dev/ttyACM0"}}, nil, false, true},
		{"gpsd mode uses gpsd", Config{Mode: ModeGPSD}, true, nil, nil, true, true},
		{"nmea opens exactly the device", Config{Mode: ModeNMEA, Device: "/dev/ttyUSB4"}, true,
			[]Device{{Path: "/dev/ttyACM0"}}, []string{"/dev/ttyUSB4"}, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &clock{now: t0}
			tr := NewTracker(c.Now)
			fo := &fakeOpen{data: validData(t)}
			g := deadGPSD()
			if tc.gpsd {
				g.Dial = func(context.Context, string) (net.Conn, error) {
					a, b := net.Pipe()
					go func() {
						bufio.NewReader(b).ReadString('\n') // net.Pipe is synchronous: take the WATCH command first
						io.WriteString(b, `{"class":"TPV","mode":3,"lat":9.9,"lon":-84.0}`+"\n")
						b.Close()
					}()
					return a, nil
				}
			}
			m := &Manager{Config: func() Config { return tc.cfg }, Tracker: tr, GPSD: g,
				Detect: func() []Device { return tc.detected }, Open: fo.Open}
			g.OnFix, g.OnState, g.Clock = tr.Update, func(up bool) {
				if up {
					tr.Arm()
				}
			}, c.Now
			m.Clock = c.Now
			m.once(context.Background(), tc.cfg, g)
			if strings.Join(fo.opened, ",") != strings.Join(tc.wantOpened, ",") {
				t.Errorf("opened %v, want %v", fo.opened, tc.wantOpened)
			}
			if _, ok := tr.Latest(); ok != tc.wantFix {
				t.Errorf("fix=%v want %v", ok, tc.wantFix)
			}
			c.now = t0.Add(time.Hour)
			if armed := tr.Silent(Config{Mode: ModeGPSD}); armed != tc.wantArmed {
				t.Errorf("armed=%v want %v", armed, tc.wantArmed)
			}
		})
	}
}

func TestManagerRunStopsOnContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tr := NewTracker(nil)
	m := &Manager{Config: func() Config { return Config{Mode: ModeOff} }, Tracker: tr,
		Sleep: func(ctx context.Context, d time.Duration) bool {
			if d != RescanEvery {
				t.Errorf("off mode waits %v", d)
			}
			cancel()
			return false
		}}
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestValidDevicePath(t *testing.T) {
	for p, want := range map[string]bool{
		"/dev/ttyACM0": true, "/dev/ttyUSB12": true, "/dev/ttyS1": true, "/dev/ttyAMA0": true,
		"/dev/sda": false, "/dev/ttyACM": false, "/dev/../etc/passwd": false, "ttyACM0": false, "/dev/ttyACM0 ": false,
	} {
		if ValidDevicePath(p) != want {
			t.Errorf("%q", p)
		}
	}
	if !DeviceForPath("/dev/ttyUSB1").USB || DeviceForPath("/dev/ttyACM1").USB {
		t.Error("USB flag")
	}
}

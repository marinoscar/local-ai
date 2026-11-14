package music_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/api/apitest"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/music"
	"github.com/marinoscar/local-ai/agent/internal/music/fake"
)

// secretLyrics carries a marker that must never appear in a log line or an event.
const secretLyrics = "[verse]\nZebra-marker walks the river\n[chorus]\nla la la"

var principals = api.AuthFunc(func(r *http.Request, _ *api.Route) (api.Principal, error) {
	switch r.Header.Get("X-Test-Principal") {
	case "op":
		return api.Principal{Kind: api.KindOperator, Credential: api.CredBearer, Via: "bearer"}, nil
	case "admin":
		return api.Principal{Kind: api.KindAdmin, Credential: api.CredSocket, Via: "uid=0"}, nil
	}
	return api.Anonymous(), nil
})

// syncBuf is a log sink safe for the service's goroutines.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type pubLog struct {
	mu  sync.Mutex
	evs []published
}

type published struct {
	typ  string
	data events.MusicOp
}

func (p *pubLog) Publish(typ, _ string, data any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.evs = append(p.evs, published{typ, data.(events.MusicOp)})
	return nil
}

func (p *pubLog) all() []published {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]published{}, p.evs...)
}

type auditLog struct {
	mu  sync.Mutex
	evs []audit.Event
}

func (a *auditLog) Write(ev audit.Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.evs = append(a.evs, ev)
	return nil
}

type rig struct {
	t     *testing.T
	shim  *fake.Shim
	svc   *music.Service
	h     http.Handler
	songs string
	work  string
	pub   *pubLog
	audit *auditLog
	logs  *syncBuf
	free  func() int64
	mu    sync.Mutex
}

func (r *rig) setFree(n int64) {
	r.mu.Lock()
	r.free = func() int64 { return n }
	r.mu.Unlock()
}

func newRig(t *testing.T, mod ...func(*music.Deps)) *rig {
	t.Helper()
	dir := t.TempDir()
	r := &rig{t: t, shim: fake.New(), songs: filepath.Join(dir, "songs"), work: filepath.Join(dir, "work"), pub: &pubLog{}, audit: &auditLog{}, logs: &syncBuf{}}
	r.free = func() int64 { return 1 << 40 }
	d := music.Deps{
		Shim: r.shim, SongsDir: r.songs, WorkDir: r.work, Publisher: r.pub, Audit: r.audit, Poll: 2 * time.Millisecond,
		MinFreeBytes: func() int64 { return 1 << 20 },
		FreeBytes: func(string) (int64, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.free(), nil
		},
		Installed: func() bool { return true },
		Logger:    slog.New(slog.NewJSONHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	for _, m := range mod {
		m(&d)
	}
	svc, err := music.New(d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	r.svc = svc
	reg := api.NewRegistry()
	if err := svc.Register(reg); err != nil {
		t.Fatal(err)
	}
	s, err := api.New(api.Options{Registry: reg, Auth: principals, Version: "test", Mode: func() string { return "online" }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	s.SetMutationsOpen(true)
	r.h = s.Handler()
	return r
}

type call struct {
	method, target, principal string
	body                      io.Reader
	ctype                     string
	header                    map[string]string
}

func (r *rig) do(c call) *httptest.ResponseRecorder {
	r.t.Helper()
	if c.method == "" {
		c.method = http.MethodGet
	}
	if c.principal == "" {
		c.principal = "op"
	}
	req := httptest.NewRequest(c.method, "/api/v1"+c.target, c.body)
	req.Host = "127.0.0.1:7070"
	req.Header.Set("X-Test-Principal", c.principal)
	if c.method != http.MethodGet {
		ct := c.ctype
		if ct == "" {
			ct = "application/json"
		}
		req.Header.Set("Content-Type", ct)
	}
	for k, v := range c.header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	apitest.RecordResult(r.t, req, rec)
	return rec
}

// form is a multipart body: text fields and an optional sample.
type form struct {
	fields map[string]string
	sample []byte
}

func (f form) encode(t *testing.T) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range f.fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if f.sample != nil {
		w, err := mw.CreateFormFile("sample", "clip.bin")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(f.sample)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

func (r *rig) generate(f form) *httptest.ResponseRecorder {
	r.t.Helper()
	body, ct := f.encode(r.t)
	return r.do(call{method: http.MethodPost, target: "/music/generate", body: body, ctype: ct})
}

func baseFields(over map[string]string) map[string]string {
	m := map[string]string{"lyrics": secretLyrics, "tags": "latin pop", "duration_s": "30", "language": "es", "seed": "7", "variations": "2"}
	for k, v := range over {
		if v == "-" {
			delete(m, k)
			continue
		}
		m[k] = v
	}
	return m
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

type errEnvelope struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

func expectError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) errEnvelope {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
	e := decode[errEnvelope](t, rec)
	if e.Error.Code != code {
		t.Fatalf("code %s, want %s: %s", e.Error.Code, code, rec.Body.String())
	}
	return e
}

// waitOp polls the operation until it is final.
func (r *rig) waitOp(opID string) music.OpView {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		v, err := r.svc.Op(opID, "en")
		if err != nil {
			r.t.Fatal(err)
		}
		switch v.State {
		case music.StateDone, music.StateFailed, music.StateCancelled:
			return v
		}
		time.Sleep(2 * time.Millisecond)
	}
	r.t.Fatalf("operation %s did not finish", opID)
	return music.OpView{}
}

// waitState polls until the operation reaches state.
func (r *rig) waitState(opID, state string) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if v, _ := r.svc.Op(opID, "en"); v.State == state {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	r.t.Fatalf("operation %s never reached %s", opID, state)
}

// songDirs lists the song directories on disk (temporary ones included).
func (r *rig) songDirs() []string {
	ents, _ := os.ReadDir(r.songs)
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// workEntries lists what is left in the work directory, the empty incoming directory excepted.
func (r *rig) workEntries() []string {
	ents, _ := os.ReadDir(r.work)
	var out []string
	for _, e := range ents {
		if e.Name() == "incoming" {
			if sub, _ := os.ReadDir(filepath.Join(r.work, "incoming")); len(sub) == 0 {
				continue
			}
		}
		out = append(out, e.Name())
	}
	return out
}

func (r *rig) noSecretsInLogs() {
	r.t.Helper()
	if strings.Contains(r.logs.String(), "Zebra") {
		r.t.Fatalf("the lyrics reached the log:\n%s", r.logs.String())
	}
}

// wavBytes is a mono 16-bit PCM WAV of the given length at 8 kHz.
func wavBytes(seconds float64) []byte { return fake.WAV(seconds, 8000) }

// flacBytes is a FLAC header whose STREAMINFO says total samples at rate.
func flacBytes(rate uint32, total uint64) []byte {
	b := []byte("fLaC")
	b = append(b, 0x80, 0, 0, 34) // last block, STREAMINFO, 34 bytes
	si := make([]byte, 34)
	si[10] = byte(rate >> 12)
	si[11] = byte(rate >> 4)
	si[12] = byte(rate<<4) | 0x02 // channels and bits, irrelevant here
	si[13] = 0x70 | byte(total>>32)&0x0F
	binary.BigEndian.PutUint32(si[14:18], uint32(total))
	return append(append(b, si...), make([]byte, 64)...)
}

// oggPage is an Ogg page with one segment of payload and the given granule position.
func oggPage(granule uint64, payload []byte) []byte {
	b := []byte("OggS")
	b = append(b, 0, 0)
	b = binary.LittleEndian.AppendUint64(b, granule)
	b = append(b, make([]byte, 12)...) // serial, sequence, checksum
	b = append(b, 1, byte(len(payload)))
	return append(b, payload...)
}

func vorbisOgg(rate uint32, samples uint64) []byte {
	id := []byte{1}
	id = append(id, "vorbis"...)
	id = append(id, 0, 0, 0, 0, 1)
	id = binary.LittleEndian.AppendUint32(id, rate)
	id = append(id, make([]byte, 14)...)
	return append(oggPage(0, id), oggPage(samples, []byte("audio"))...)
}

func opusOgg(samples uint64) []byte {
	id := []byte("OpusHead")
	id = append(id, 1, 2)
	id = binary.LittleEndian.AppendUint16(id, 312)
	id = append(id, make([]byte, 7)...)
	return append(oggPage(0, id), oggPage(samples+312, []byte("audio"))...)
}

// mp3CBR is an MPEG-1 layer III stream at 128 kbit/s, 44.1 kHz, of about the given length.
func mp3CBR(seconds float64) []byte {
	n := int(seconds * 128000 / 8)
	b := make([]byte, n)
	copy(b, []byte{0xFF, 0xFB, 0x90, 0x00})
	return b
}

// mp3Xing is an MPEG-1 layer III file whose Xing header counts frames.
func mp3Xing(frames uint32) []byte {
	b := []byte("ID3")
	b = append(b, 4, 0, 0, 0, 0, 0, 0) // an empty ID3v2 tag
	frame := make([]byte, 417)
	copy(frame, []byte{0xFF, 0xFB, 0x90, 0x00})
	copy(frame[4+32:], "Xing")
	binary.BigEndian.PutUint32(frame[4+32+4:], 1)
	binary.BigEndian.PutUint32(frame[4+32+8:], frames)
	return append(b, frame...)
}

package jobs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/music"
	musicfake "github.com/marinoscar/local-ai/agent/internal/music/fake"
)

const reqID = "0b5f3a52-7a52-4c1e-9d55-0d6f1c2a9e11"

// ---------------------------------------------------------------------------------------------------
// Decoders

func TestDecodeWorkloadActivate(t *testing.T) {
	for _, c := range []struct {
		raw  string
		want WorkloadActivateParams
		err  string
	}{
		{`{"name":"music"}`, WorkloadActivateParams{Name: "music"}, ""},
		{`{"name":"chat-photos","force":true}`, WorkloadActivateParams{Name: "chat-photos", Force: true}, ""},
		{`{}`, WorkloadActivateParams{}, "name: is required"},
		{`{"name":"Music"}`, WorkloadActivateParams{}, "name: must be a workload name"},
		{`{"name":"music","wait":true}`, WorkloadActivateParams{}, "wait: unknown field"},
		{`{"name":"music","Force":true}`, WorkloadActivateParams{}, "Force: unknown field"},
		{`{"name":"music","force":null}`, WorkloadActivateParams{}, "force: must not be null"},
		{`{"name":3}`, WorkloadActivateParams{}, "name: must be a string"},
	} {
		got, err := decodeWorkloadActivate(json.RawMessage(c.raw))
		if c.err != "" {
			var de *cloud.DecodeError
			if !errors.As(err, &de) || !strings.HasPrefix(err.Error(), c.err) {
				t.Errorf("%s: err %v, want %q", c.raw, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: %+v, %v", c.raw, got, err)
		}
	}
}

func validMusicParams(over map[string]any) map[string]any {
	m := map[string]any{
		"request_id": reqID, "lyrics": "[verse]\nla la", "tags": "folk", "duration_s": 30, "language": "es", "variations": 2,
		"results": []any{map[string]any{"put_url": "https://storage.example.test/a?sig=x"}, map[string]any{"put_url": "https://storage.example.test/b?sig=y"}},
	}
	for k, v := range over {
		if v == nil {
			delete(m, k)
			continue
		}
		m[k] = v
	}
	return m
}

func sampleBlock(over map[string]any) map[string]any {
	m := map[string]any{"url": "https://storage.example.test/s?sig=z", "sha256": strings.Repeat("ab", 32), "mime": "audio/wav", "size_bytes": 1000}
	for k, v := range over {
		if v == nil {
			delete(m, k)
			continue
		}
		m[k] = v
	}
	return m
}

func TestDecodeMusicGenerate(t *testing.T) {
	ok := []map[string]any{
		validMusicParams(nil),
		validMusicParams(map[string]any{"seed": 7, "sample": sampleBlock(nil), "sample_mode": "same_song", "strength": 0.8}),
		validMusicParams(map[string]any{"lyrics": "", "tags": "ambient"}),
	}
	for i, m := range ok {
		raw, _ := json.Marshal(m)
		if _, err := decodeMusicGenerate(raw); err != nil {
			t.Errorf("valid %d: %v", i, err)
		}
	}
	bad := map[string]map[string]any{
		"unknown field":            validMusicParams(map[string]any{"tempo": 120}),
		"unknown field in sample":  validMusicParams(map[string]any{"sample": sampleBlock(map[string]any{"name": "x"})}),
		"unknown field in results": validMusicParams(map[string]any{"results": []any{map[string]any{"put_url": "https://a.example.test/x", "get_url": "https://a.example.test/y"}, map[string]any{"put_url": "https://a.example.test/z"}}}),
		"missing request id":       validMusicParams(map[string]any{"request_id": nil}),
		"bad request id":           validMusicParams(map[string]any{"request_id": "nope"}),
		"lyrics too long":          validMusicParams(map[string]any{"lyrics": strings.Repeat("a", 8193)}),
		"duration":                 validMusicParams(map[string]any{"duration_s": 5}),
		"language":                 validMusicParams(map[string]any{"language": "spa"}),
		"seed":                     validMusicParams(map[string]any{"seed": 2147483648}),
		"null seed":                validMusicParams(map[string]any{"seed": "x"}),
		"variations":               validMusicParams(map[string]any{"variations": 5}),
		"results count":            validMusicParams(map[string]any{"variations": 1}),
		"http put url":             validMusicParams(map[string]any{"results": []any{map[string]any{"put_url": "http://a.example.test/x"}, map[string]any{"put_url": "https://a.example.test/z"}}}),
		"http sample url":          validMusicParams(map[string]any{"sample": sampleBlock(map[string]any{"url": "http://a.example.test/s"})}),
		"bad sha256":               validMusicParams(map[string]any{"sample": sampleBlock(map[string]any{"sha256": strings.Repeat("AB", 32)})}),
		"bad mime":                 validMusicParams(map[string]any{"sample": sampleBlock(map[string]any{"mime": "video/mp4"})}),
		"sample too large":         validMusicParams(map[string]any{"sample": sampleBlock(map[string]any{"size_bytes": 21 << 20})}),
		"sample missing sha":       validMusicParams(map[string]any{"sample": sampleBlock(map[string]any{"sha256": nil})}),
		"mode without sample":      validMusicParams(map[string]any{"sample_mode": "same_song"}),
		"strength without sample":  validMusicParams(map[string]any{"strength": 0.5}),
		"strength out of range":    validMusicParams(map[string]any{"sample": sampleBlock(nil), "strength": 1.5}),
	}
	for name, m := range bad {
		raw, _ := json.Marshal(m)
		if _, err := decodeMusicGenerate(raw); err == nil {
			t.Errorf("%s: accepted", name)
		} else if de := (*cloud.DecodeError)(nil); !errors.As(err, &de) {
			t.Errorf("%s: %T is not a DecodeError", name, err)
		}
	}
	// A null for an optional key is refused, as Zod's .optional() does.
	raw := []byte(strings.Replace(string(mustJSON(validMusicParams(nil))), `"tags"`, `"seed":null,"tags"`, 1))
	if _, err := decodeMusicGenerate(raw); err == nil {
		t.Error("seed: null accepted")
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// ---------------------------------------------------------------------------------------------------
// workload.activate

type fakeWorkloads struct {
	name  string
	force bool
	actor audit.Actor
	err   error
}

func (f *fakeWorkloads) Activate(_ context.Context, name string, force bool, actor audit.Actor) (string, error) {
	f.name, f.force, f.actor = name, force, actor
	if f.err != nil {
		return "", f.err
	}
	return name, nil
}

func TestWorkloadActivate(t *testing.T) {
	f := newFakes()
	w := &fakeWorkloads{}
	d := f.deps()
	d.Workloads = w
	h := f.handlerOf(t, d, cloud.TypeWorkloadActivate)
	if h.Cancellable() {
		t.Error("an activation carries on when the node goes offline")
	}
	params, err := h.Decode(json.RawMessage(`{"name":"music"}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.Run(context.Background(), job("job-w"), params)
	if err != nil {
		t.Fatal(err)
	}
	if got := resultJSON(t, cloud.TypeWorkloadActivate, out); got != `{"active":"music"}` {
		t.Fatalf("result %s", got)
	}
	if w.name != "music" || w.force || w.actor.Kind != audit.KindCloud || w.actor.JobID != "job-w" {
		t.Fatalf("call %+v", w)
	}
	// Survival mode without force: the agent's LAI-LLM-053 is the job's code.
	w.err = api.NewError(doctor.LLM053, http.StatusConflict, nil)
	_, err = h.Run(context.Background(), job("job-w2"), params)
	if code, _ := cloud.CodeAndMessage(err); code != doctor.LLM053 {
		t.Fatalf("error %v", err)
	}
	// Without the workloads engine the type is not offered.
	for _, x := range Handlers(f.deps()) {
		if x.Type() == cloud.TypeWorkloadActivate || x.Type() == cloud.TypeMusicGenerate {
			t.Fatalf("%s offered without its dependency", x.Type())
		}
	}
}

// ---------------------------------------------------------------------------------------------------
// music.generate

// blobs is a fake object store behind https: GET of the sample, PUT of the songs.
type blobs struct {
	mu        sync.Mutex
	sample    []byte
	getStatus int
	putFail   int // the first putFail PUTs answer putStatus
	putStatus int
	puts      int
	stored    map[string][]byte
	ctype     map[string]string
}

func (b *blobs) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch r.Method {
	case http.MethodGet:
		if b.getStatus != 0 {
			w.WriteHeader(b.getStatus)
			return
		}
		_, _ = w.Write(b.sample)
	case http.MethodPut:
		b.puts++
		body, _ := io.ReadAll(r.Body)
		if b.putFail > 0 {
			b.putFail--
			w.WriteHeader(b.putStatus)
			return
		}
		b.stored[r.URL.Path] = body
		b.ctype[r.URL.Path] = r.Header.Get("Content-Type")
	}
}

type musicRig struct {
	t    *testing.T
	shim *musicfake.Shim
	svc  *music.Service
	srv  *httptest.Server
	blob *blobs
	logs *lockedBuf
	h    cloud.Handler
	dir  string
}

type lockedBuf struct {
	mu sync.Mutex
	*bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Buffer.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Buffer.String()
}

func newMusicRig(t *testing.T) *musicRig {
	t.Helper()
	oldB, oldA := transferBackoff, transferAttempts
	transferBackoff, transferAttempts = []time.Duration{time.Millisecond}, 3
	t.Cleanup(func() { transferBackoff, transferAttempts = oldB, oldA })
	r := &musicRig{t: t, shim: musicfake.New(), blob: &blobs{stored: map[string][]byte{}, ctype: map[string]string{}}, logs: &lockedBuf{Buffer: &bytes.Buffer{}}, dir: t.TempDir()}
	r.shim.AutoComplete = true
	r.srv = httptest.NewTLSServer(r.blob)
	t.Cleanup(r.srv.Close)
	svc, err := music.New(music.Deps{
		Shim: r.shim, SongsDir: filepath.Join(r.dir, "songs"), WorkDir: filepath.Join(r.dir, "work"), Poll: 2 * time.Millisecond,
		FreeBytes: func(string) (int64, error) { return 1 << 40, nil }, Installed: func() bool { return true },
		Logger: slog.New(slog.NewTextHandler(r.logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	r.svc = svc
	f := newFakes()
	d := f.deps()
	d.Music, d.MusicHTTP = svc, r.srv.Client()
	r.h = f.handlerOf(t, d, cloud.TypeMusicGenerate)
	return r
}

func (r *musicRig) params(over map[string]any) map[string]any {
	return validMusicParams(mergeMaps(map[string]any{"results": []any{
		map[string]any{"put_url": r.srv.URL + "/songs/0?sig=secret-token"}, map[string]any{"put_url": r.srv.URL + "/songs/1?sig=secret-token"},
	}}, over))
}

func mergeMaps(a, b map[string]any) map[string]any {
	for k, v := range b {
		a[k] = v
	}
	return a
}

func (r *musicRig) withSample(data []byte, over map[string]any) map[string]any {
	sum := sha256.Sum256(data)
	r.blob.sample = data
	s := map[string]any{"url": r.srv.URL + "/sample?sig=secret-token", "sha256": hex.EncodeToString(sum[:]), "mime": "audio/wav", "size_bytes": len(data)}
	for k, v := range over {
		s[k] = v
	}
	return r.params(map[string]any{"sample": s, "sample_mode": "sounds_like"})
}

func (r *musicRig) run(p map[string]any) (cloud.Outcome, error) {
	r.t.Helper()
	params, err := r.h.Decode(mustJSON(p))
	if err != nil {
		r.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := r.h.Run(ctx, cloud.Job{ID: "job-m", Actor: audit.Cloud("job-m"), Logger: slog.New(slog.NewTextHandler(r.logs, nil))}, params)
	if strings.Contains(r.logs.String(), "secret-token") || strings.Contains(r.logs.String(), "la la") {
		r.t.Fatalf("a signed URL or the lyrics reached the log:\n%s", r.logs.String())
	}
	if err != nil {
		if _, msg := cloud.CodeAndMessage(err); strings.Contains(msg, "secret-token") || strings.Contains(msg, r.srv.URL) {
			r.t.Fatalf("the failure names a URL: %s", msg)
		}
	}
	return out, err
}

func (r *musicRig) songs() []music.Meta {
	all, _ := r.svc.Store().All()
	return all
}

func expectCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want %s", code)
	}
	if got, _ := cloud.CodeAndMessage(err); got != code {
		t.Fatalf("code %s (%v), want %s", got, err, code)
	}
}

func TestMusicGenerateHappyPaths(t *testing.T) {
	for _, withSample := range []bool{false, true} {
		t.Run(fmt.Sprintf("sample=%v", withSample), func(t *testing.T) {
			r := newMusicRig(t)
			p := r.params(nil)
			if withSample {
				p = r.withSample(musicfake.WAV(2, 8000), nil)
			}
			out, err := r.run(p)
			if err != nil {
				t.Fatal(err)
			}
			raw := resultJSON(t, cloud.TypeMusicGenerate, out)
			var res MusicGenerateResult
			_ = json.Unmarshal([]byte(raw), &res)
			if len(res.Songs) != 2 || res.Songs[1].Index != 1 || res.Songs[0].Mime != "audio/wav" {
				t.Fatalf("result %s", raw)
			}
			for i, s := range res.Songs {
				body := r.blob.stored[fmt.Sprintf("/songs/%d", i)]
				sum := sha256.Sum256(body)
				if hex.EncodeToString(sum[:]) != s.SHA256 || int64(len(body)) != s.SizeBytes || r.blob.ctype[fmt.Sprintf("/songs/%d", i)] != "audio/wav" {
					t.Fatalf("upload %d does not match the result", i)
				}
			}
			songs := r.songs()
			if len(songs) != 2 || songs[0].Origin != music.OriginCloud || songs[0].CloudRequestID == nil || *songs[0].CloudRequestID != reqID {
				t.Fatalf("local copies %+v", songs)
			}
			if req := r.shim.Requests[0]; (req.SamplePath != nil) != withSample {
				t.Fatalf("sample passed: %v", req.SamplePath)
			}
		})
	}
}

func TestMusicGenerateFailures(t *testing.T) {
	sample := musicfake.WAV(1, 8000)
	for _, c := range []struct {
		name      string
		setup     func(r *musicRig)
		params    func(r *musicRig) map[string]any
		code      string
		songs     int
		puts      int
		generated bool
	}{
		{
			name: "bad sha256", code: doctor.SVC156,
			params: func(r *musicRig) map[string]any {
				return r.withSample(sample, map[string]any{"sha256": strings.Repeat("0", 64)})
			},
		},
		{
			name: "wrong size", code: doctor.SVC156,
			params: func(r *musicRig) map[string]any {
				return r.withSample(sample, map[string]any{"size_bytes": len(sample) + 10})
			},
		},
		{
			name: "sample gone", code: doctor.SVC156, setup: func(r *musicRig) { r.blob.getStatus = http.StatusForbidden },
			params: func(r *musicRig) map[string]any { return r.withSample(sample, nil) },
		},
		{
			name: "sample of another type", code: doctor.SVC154,
			params: func(r *musicRig) map[string]any { return r.withSample([]byte("not audio at all"), nil) },
		},
		{
			name: "music not loaded", code: doctor.SVC151, setup: func(r *musicRig) { r.shim.Status = music.HealthLoading },
			params: func(r *musicRig) map[string]any { return r.withSample(sample, nil) },
		},
		{
			name: "engine failure", code: doctor.SVC152, setup: func(r *musicRig) { r.shim.AutoComplete, r.shim.FailOnPoll = false, true },
			params: func(r *musicRig) map[string]any { return r.params(nil) },
		},
		{
			name: "PUT fails then succeeds", code: "", songs: 2, puts: 4, generated: true,
			setup:  func(r *musicRig) { r.blob.putFail, r.blob.putStatus = 2, http.StatusServiceUnavailable },
			params: func(r *musicRig) map[string]any { return r.params(nil) },
		},
		{
			name: "PUT fails after retries", code: doctor.SVC157, songs: 2, puts: 3, generated: true,
			setup:  func(r *musicRig) { r.blob.putFail, r.blob.putStatus = 10, http.StatusBadGateway },
			params: func(r *musicRig) map[string]any { return r.params(nil) },
		},
		{
			name: "PUT refused is not retried", code: doctor.SVC157, songs: 2, puts: 1, generated: true,
			setup:  func(r *musicRig) { r.blob.putFail, r.blob.putStatus = 10, http.StatusForbidden },
			params: func(r *musicRig) map[string]any { return r.params(nil) },
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newMusicRig(t)
			if c.setup != nil {
				c.setup(r)
			}
			_, err := r.run(c.params(r))
			if c.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				expectCode(t, err, c.code)
			}
			if got := len(r.songs()); got != c.songs {
				t.Fatalf("%d local songs, want %d", got, c.songs)
			}
			if r.blob.puts != c.puts {
				t.Fatalf("%d PUTs, want %d", r.blob.puts, c.puts)
			}
			if (len(r.shim.Requests) > 0) != c.generated && c.code != doctor.SVC152 {
				t.Fatalf("generated: %v", len(r.shim.Requests) > 0)
			}
			if ents, _ := os.ReadDir(filepath.Join(r.dir, "work", "incoming")); len(ents) != 0 {
				t.Fatalf("a staged sample was left: %v", ents)
			}
		})
	}
}

func TestMusicGenerateCancelledByTheClaimLoop(t *testing.T) {
	r := newMusicRig(t)
	r.shim.AutoComplete = false
	params, err := r.h.Decode(mustJSON(r.params(nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := r.h.Run(ctx, cloud.Job{ID: "job-c", Actor: audit.Cloud("job-c"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, params)
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(r.shim.IDs()) == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	expectCode(t, <-done, doctor.SVC158)
	if len(r.shim.Cancelled) != 1 {
		t.Fatal("the local operation was not cancelled")
	}
}

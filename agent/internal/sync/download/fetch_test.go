package download

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// startOf parses "bytes=N-" (0 when there is no Range header).
func startOf(r *http.Request) int {
	v := strings.TrimSuffix(strings.TrimPrefix(r.Header.Get("Range"), "bytes="), "-")
	n, _ := strconv.Atoi(v)
	return n
}

// cutHandler answers the first `aborts` requests with the whole remaining file announced but only
// `slice` bytes sent, then the connection dropped (the client sees an unexpected EOF); later
// requests fall through to the normal server.
func cutHandler(data []byte, slice, aborts int) func(int, http.ResponseWriter, *http.Request) bool {
	return func(n int, w http.ResponseWriter, r *http.Request) bool {
		if n >= aborts {
			return false
		}
		start := startOf(r)
		w.Header().Set("ETag", `"v1"`)
		if start > 0 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(data)-1, len(data)))
			w.Header().Set("Content-Length", strconv.Itoa(len(data)-start))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		}
		end := min(start+slice, len(data))
		_, _ = w.Write(data[start:end])
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
}

// statusHandler answers the first requests with the given statuses (and headers), then falls
// through.
func statusHandler(codes []int, hdr map[string]string) func(int, http.ResponseWriter, *http.Request) bool {
	return func(n int, w http.ResponseWriter, r *http.Request) bool {
		if n >= len(codes) {
			return false
		}
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.WriteHeader(codes[n])
		return true
	}
}

func TestFetchResumeTable(t *testing.T) {
	data := genData(testSize, 1)
	const ck = 2 * testCkpt // 64 KiB: a checkpoint offset
	for _, tc := range []struct {
		name         string
		prep         func(e *env, srv *testServer, j *FileJob)
		wantRange    string // Range of the first request ("" = none)
		wantReqs     int
		wantFrom     int64
		wantRestart  bool
		wantNotice   bool
		wantIfRange  string
		wantNoReq    bool
		wantSleeps   int
		wantBucket   int64 // bytes drawn from the bucket
		wantCkptGone bool
	}{
		{name: "fresh file", wantReqs: 1, wantBucket: testSize},
		{
			name:      "206 resume from the checkpoint",
			prep:      func(e *env, s *testServer, j *FileJob) { e.seedPart(j.URL, data, ck, ck) },
			wantRange: "bytes=65536-", wantIfRange: `"v1"`, wantReqs: 1, wantFrom: ck, wantBucket: testSize - ck,
		},
		{
			name:      "part longer than the checkpoint is cut back to it",
			prep:      func(e *env, s *testServer, j *FileJob) { e.seedPart(j.URL, data, ck+12345, ck) },
			wantRange: "bytes=65536-", wantIfRange: `"v1"`, wantReqs: 1, wantFrom: ck, wantBucket: testSize - ck,
		},
		{
			name:     "part shorter than the checkpoint restarts from zero",
			prep:     func(e *env, s *testServer, j *FileJob) { e.seedPart(j.URL, data, 20000, ck) },
			wantReqs: 1, wantBucket: testSize,
		},
		{
			name:     "part with no checkpoint restarts from zero",
			prep:     func(e *env, s *testServer, j *FileJob) { e.seedPart(j.URL, data, 50000, 0) },
			wantReqs: 1, wantBucket: testSize,
		},
		{
			name: "checkpoint of another URL restarts from zero",
			prep: func(e *env, s *testServer, j *FileJob) {
				e.seedPart(j.URL, data, ck, ck)
				cp, _ := e.cp.Get(testName)
				cp.URL = "https://elsewhere.example.test/file"
				_ = e.cp.Save(testName, cp)
			},
			wantReqs: 1, wantBucket: testSize,
		},
		{
			name: "hash state of another offset restarts from zero",
			prep: func(e *env, s *testServer, j *FileJob) {
				e.seedPart(j.URL, data, ck+8192, ck)
				cp, _ := e.cp.Get(testName)
				cp.Offset = ck + 4096 // the saved state still covers only 65536 bytes
				_ = e.cp.Save(testName, cp)
			},
			wantReqs: 1, wantBucket: testSize,
		},
		{
			name:      "server ignores Range: the file restarts from zero (info)",
			prep:      func(e *env, s *testServer, j *FileJob) { s.noRange = true; e.seedPart(j.URL, data, ck, ck) },
			wantRange: "bytes=65536-", wantIfRange: `"v1"`, wantReqs: 1, wantRestart: true, wantNotice: true, wantFrom: ck, wantBucket: testSize,
		},
		{
			name: "If-Range mismatch (the file changed): the file restarts from zero (info)",
			prep: func(e *env, s *testServer, j *FileJob) {
				e.seedPart(j.URL, data, ck, ck)
				cp, _ := e.cp.Get(testName)
				cp.ETag = `"v0"`
				_ = e.cp.Save(testName, cp)
			},
			wantRange: "bytes=65536-", wantIfRange: `"v0"`, wantReqs: 1, wantRestart: true, wantNotice: true, wantFrom: ck, wantBucket: testSize,
		},
		{
			name: "checkpoint at the full size needs no request",
			prep: func(e *env, s *testServer, j *FileJob) {
				e.seedPart(j.URL, data, len(data), len(data))
			},
			wantNoReq: true, wantFrom: testSize,
		},
		{
			name: "416 with the offset at the full size completes (size not known up front)",
			prep: func(e *env, s *testServer, j *FileJob) {
				e.seedPart(j.URL, data, len(data), len(data))
				j.ExpectedSize = 0
			},
			wantRange: "bytes=204800-", wantIfRange: `"v1"`, wantReqs: 1, wantFrom: testSize,
		},
		{
			name: "a checkpoint beyond the expected size restarts from zero",
			prep: func(e *env, s *testServer, j *FileJob) {
				e.seedPart(j.URL, data, ck, ck)
				j.ExpectedSize, j.ExpectedSHA256 = 30000, sum(data[:30000])
				s.setData(data[:30000], `"v1"`)
			},
			wantReqs: 1, wantBucket: 30000,
		},
		{
			name: "416 with the offset beyond the file restarts from zero",
			prep: func(e *env, s *testServer, j *FileJob) {
				short := data[:30000]
				e.seedPart(j.URL, data, ck, ck)
				s.setData(short, `"v1"`)
				j.ExpectedSize, j.ExpectedSHA256 = 0, sum(short)
			},
			wantRange: "bytes=65536-", wantIfRange: `"v1"`, wantReqs: 2, wantFrom: ck, wantBucket: 30000,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := data
			e := newEnv(t, nil)
			srv := newTestServer(t, data)
			j := e.job(srv.URL+"/file", data)
			if tc.prep != nil {
				tc.prep(e, srv, &j)
			}
			want := srv.data // whatever the server serves at the end is the truth
			res := mustFetch(t, e, j)
			got := e.readPart()
			if string(got) != string(want) {
				t.Fatalf(".part holds %d bytes that differ from the file (%d bytes)", len(got), len(want))
			}
			if res.SHA256 != sum(want) || res.Bytes != int64(len(want)) {
				t.Errorf("Result = %+v", res)
			}
			reqs := srv.requests()
			if tc.wantNoReq {
				if len(reqs) != 0 {
					t.Errorf("made %d requests, want none", len(reqs))
				}
			} else {
				if tc.wantReqs != 0 && len(reqs) != tc.wantReqs {
					t.Fatalf("requests = %d, want %d: %+v", len(reqs), tc.wantReqs, reqs)
				}
				if reqs[0].Range != tc.wantRange {
					t.Errorf("Range = %q, want %q", reqs[0].Range, tc.wantRange)
				}
				if reqs[0].IfRange != tc.wantIfRange {
					t.Errorf("If-Range = %q, want %q", reqs[0].IfRange, tc.wantIfRange)
				}
			}
			if res.ResumedFrom != tc.wantFrom {
				t.Errorf("ResumedFrom = %d, want %d", res.ResumedFrom, tc.wantFrom)
			}
			if res.Restarted != tc.wantRestart {
				t.Errorf("Restarted = %v, want %v", res.Restarted, tc.wantRestart)
			}
			if has := len(res.Notices) == 1 && res.Notices[0] == CodeRestart; has != tc.wantNotice {
				t.Errorf("Notices = %v, want the restart notice: %v", res.Notices, tc.wantNotice)
			}
			if tc.wantBucket != 0 && e.bucket.total() != tc.wantBucket {
				t.Errorf("bucket drew %d bytes, want %d", e.bucket.total(), tc.wantBucket)
			}
			if cp, ok := e.cp.Get(testName); !ok || cp.Offset != int64(len(want)) {
				t.Errorf("final checkpoint = %+v (present %v), want offset %d", cp, ok, len(want))
			}
		})
	}
}

func TestFetchStatusTable(t *testing.T) {
	data := genData(testSize, 2)
	rate := map[string]string{"Retry-After": "7"}
	for _, tc := range []struct {
		name       string
		codes      []int // the statuses of the first requests, then the real file
		hdr        map[string]string
		auth       bool
		wantOK     bool
		wantKind   Kind
		wantCode   string
		wantReqs   int
		wantSleeps []time.Duration
	}{
		{name: "404 is permanent", codes: []int{404}, wantKind: KindPermanent, wantCode: CodeRefused, wantReqs: 1},
		{name: "410 is permanent", codes: []int{410}, wantKind: KindPermanent, wantCode: CodeRefused, wantReqs: 1},
		{name: "403 elsewhere is permanent", codes: []int{403}, wantKind: KindPermanent, wantCode: CodeRefused, wantReqs: 1},
		{name: "401 elsewhere is permanent", codes: []int{401}, wantKind: KindPermanent, wantCode: CodeRefused, wantReqs: 1},
		{name: "401 on Hugging Face needs a token", codes: []int{401}, auth: true, wantKind: KindPermanent, wantCode: CodeHFAuth, wantReqs: 1},
		{name: "403 on Hugging Face needs a token", codes: []int{403}, auth: true, wantKind: KindPermanent, wantCode: CodeHFAuth, wantReqs: 1},
		{name: "any other 4xx is permanent", codes: []int{418}, wantKind: KindPermanent, wantCode: CodeRefused, wantReqs: 1},
		{name: "408 then success", codes: []int{408}, wantOK: true, wantReqs: 2, wantSleeps: []time.Duration{5 * time.Second}},
		{name: "500 then 502 then success", codes: []int{500, 502}, wantOK: true, wantReqs: 3, wantSleeps: []time.Duration{5 * time.Second, 15 * time.Second}},
		{name: "429 with Retry-After in seconds is honoured", codes: []int{429}, hdr: rate, wantOK: true, wantReqs: 2, wantSleeps: []time.Duration{7 * time.Second}},
		{name: "503 with Retry-After is honoured", codes: []int{503}, hdr: rate, wantOK: true, wantReqs: 2, wantSleeps: []time.Duration{7 * time.Second}},
		{
			name: "Retry-After as an HTTP date is read against the clock", codes: []int{429},
			hdr: map[string]string{"Retry-After": epoch.Add(30 * time.Second).UTC().Format(http.TimeFormat)}, wantOK: true, wantReqs: 2,
			wantSleeps: []time.Duration{30 * time.Second},
		},
		{
			name: "Retry-After beyond 15 minutes fails the file for this run", codes: []int{429},
			hdr: map[string]string{"Retry-After": "901"}, wantKind: KindExhausted, wantCode: CodeRetries, wantReqs: 1,
		},
		{
			name: "Retry-After of exactly 15 minutes is honoured", codes: []int{429},
			hdr: map[string]string{"Retry-After": "900"}, wantOK: true, wantReqs: 2, wantSleeps: []time.Duration{15 * time.Minute},
		},
		{
			name: "seven failures exhaust the retries", codes: []int{500, 500, 500, 500, 500, 500, 500, 500},
			wantKind: KindExhausted, wantCode: CodeRetries, wantReqs: 7,
			wantSleeps: []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute},
		},
		{
			name: "six failures then success", codes: []int{500, 500, 500, 500, 500, 500}, wantOK: true, wantReqs: 7,
			wantSleeps: []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute},
		},
		{
			name: "a Hugging Face rate limit that never lifts ends in the Hugging Face code", codes: []int{429, 429, 429, 429, 429, 429, 429},
			auth: true, wantKind: KindExhausted, wantCode: CodeHFAuth, wantReqs: 7,
			wantSleeps: []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, nil)
			srv := newTestServer(t, data)
			srv.handler = statusHandler(tc.codes, tc.hdr)
			j := e.job(srv.URL+"/file", data)
			j.Auth = tc.auth
			res, err := e.dl.Fetch(context.Background(), j, e.cp)
			if got := srv.count(); got != tc.wantReqs {
				t.Errorf("requests = %d, want %d", got, tc.wantReqs)
			}
			if !reflect.DeepEqual(e.clk.delays(), tc.wantSleeps) && !(len(e.clk.delays()) == 0 && len(tc.wantSleeps) == 0) {
				t.Errorf("waits = %v, want %v", e.clk.delays(), tc.wantSleeps)
			}
			if tc.wantOK {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				if res.Code != "" || string(e.readPart()) != string(data) {
					t.Errorf("Result = %+v", res)
				}
				return
			}
			var fe *Error
			if !errors.As(err, &fe) {
				t.Fatalf("err = %v, want *Error", err)
			}
			if fe.Kind != tc.wantKind || fe.Code != tc.wantCode || res.Code != tc.wantCode {
				t.Errorf("error = %+v, result code %q; want %s %s", fe, res.Code, tc.wantKind, tc.wantCode)
			}
			if res.Permanent != (tc.wantKind == KindPermanent) {
				t.Errorf("Permanent = %v for %s", res.Permanent, tc.wantKind)
			}
			if !errors.Is(err, &Error{Kind: tc.wantKind}) {
				t.Errorf("errors.Is(%v, sentinel %s) is false", err, tc.wantKind)
			}
			if _, statErr := os.Stat(e.partPath()); statErr != nil {
				t.Errorf("the .part must stay: %v", statErr)
			}
		})
	}
}

func TestFetchTruncatedBodyResumesFromTheExactOffset(t *testing.T) {
	data := genData(testSize, 3)
	e := newEnv(t, nil)
	srv := newTestServer(t, data)
	srv.handler = cutHandler(data, 70000, 1)
	res := mustFetch(t, e, e.job(srv.URL+"/f", data))
	reqs := srv.requests()
	if len(reqs) != 2 || reqs[0].Range != "" || reqs[1].Range != "bytes=70000-" {
		t.Fatalf("requests = %+v, want a plain one and a resume at 70000", reqs)
	}
	if string(e.readPart()) != string(data) || res.Attempts != 2 || res.ResumedFrom != 0 {
		t.Errorf("Result = %+v", res)
	}
	if got := e.clk.delays(); len(got) != 1 || got[0] != 5*time.Second {
		t.Errorf("waits = %v", got)
	}
}

func TestFetchTruncatedForeverKeepsPartAndCheckpoint(t *testing.T) {
	data := genData(testSize, 4)
	e := newEnv(t, nil)
	srv := newTestServer(t, data)
	srv.handler = cutHandler(data, 10000, 100)
	res, err := e.dl.Fetch(context.Background(), e.job(srv.URL+"/f", data), e.cp)
	var fe *Error
	if !errors.As(err, &fe) || fe.Kind != KindExhausted || fe.Code != CodeRetries {
		t.Fatalf("err = %v, want exhausted %s", err, CodeRetries)
	}
	if res.Attempts != 7 || srv.count() != 7 {
		t.Errorf("attempts = %d, requests = %d, want 7", res.Attempts, srv.count())
	}
	if res.Bytes != 70000 {
		t.Errorf("Bytes = %d, want 70000 (7 x 10000)", res.Bytes)
	}
	cp, ok := e.cp.Get(testName)
	if !ok || cp.Offset != 70000 {
		t.Errorf("checkpoint = %+v (present %v), want offset 70000", cp, ok)
	}
	if got := e.readPart(); string(got) != string(data[:70000]) {
		t.Errorf(".part holds %d bytes, want the first 70000 intact", len(got))
	}
	// The next run resumes from there.
	srv.handler = nil
	e.clk.sleeps = nil
	res2 := mustFetch(t, e, e.job(srv.URL+"/f", data))
	if res2.ResumedFrom != 70000 || string(e.readPart()) != string(data) {
		t.Errorf("second run: %+v", res2)
	}
}

func TestFetchProgressResetsTheBackoff(t *testing.T) {
	data := genData(1<<20, 5)
	// Every attempt moves 70000 bytes (more than ProgressReset = 64 KiB in the test environment):
	// 15 attempts are needed, far more than the 7 the schedule allows, and every wait is the first.
	e := newEnv(t, nil)
	srv := newTestServer(t, data)
	srv.handler = cutHandler(data, 70000, 14)
	res := mustFetch(t, e, e.job(srv.URL+"/f", data))
	if res.Attempts != 15 {
		t.Errorf("attempts = %d, want 15", res.Attempts)
	}
	for i, d := range e.clk.delays() {
		if d != 5*time.Second {
			t.Errorf("wait %d = %v, want 5s (progress resets the schedule)", i, d)
		}
	}
	if string(e.readPart()) != string(data) {
		t.Error(".part differs")
	}
}

func TestFetchMismatchTable(t *testing.T) {
	data := genData(testSize, 6)
	flipped := append([]byte(nil), data...)
	flipped[1000] ^= 0xFF
	for _, tc := range []struct {
		name     string
		serve    func(s *testServer)
		wantCode string
		wantPart int
		wantReqs int
	}{
		{
			name:     "checksum mismatch",
			serve:    func(s *testServer) { s.setData(flipped, `"v1"`) },
			wantCode: CodeChecksum, wantPart: testSize, wantReqs: 1,
		},
		{
			name: "the body ends early with a clean EOF",
			serve: func(s *testServer) {
				s.handler = func(n int, w http.ResponseWriter, r *http.Request) bool {
					_, _ = w.Write(data[:100000])
					w.(http.Flusher).Flush() // no Content-Length: chunked, so the end is clean
					return true
				}
			},
			wantCode: CodeSize, wantPart: 100000, wantReqs: 1,
		},
		{
			name: "the body is longer than expected",
			serve: func(s *testServer) {
				s.handler = func(n int, w http.ResponseWriter, r *http.Request) bool {
					_, _ = w.Write(append(append([]byte(nil), data...), genData(5000, 9)...))
					return true
				}
			},
			wantCode: CodeSize, wantPart: testSize, wantReqs: 1,
		},
		{
			name:     "the server announces another size: nothing is downloaded",
			serve:    func(s *testServer) { s.setData(data[:len(data)-10], `"v1"`) },
			wantCode: CodeSize, wantPart: 0, wantReqs: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, nil)
			srv := newTestServer(t, data)
			tc.serve(srv)
			res, err := e.dl.Fetch(context.Background(), e.job(srv.URL+"/f", data), e.cp)
			var fe *Error
			if !errors.As(err, &fe) || fe.Kind != KindMismatch || fe.Code != tc.wantCode {
				t.Fatalf("err = %v, want a %s mismatch", err, tc.wantCode)
			}
			if !res.Mismatch || res.Code != tc.wantCode || !res.Permanent {
				t.Errorf("Result = %+v", res)
			}
			if got := e.readPart(); len(got) != tc.wantPart {
				t.Errorf(".part has %d bytes, want %d (kept for the caller to quarantine)", len(got), tc.wantPart)
			}
			if _, ok := e.cp.Get(testName); ok {
				t.Error("the checkpoint of rejected bytes must be cleared")
			}
			if srv.count() != tc.wantReqs {
				t.Errorf("requests = %d (a mismatch is never retried in the same run)", srv.count())
			}
			if tc.wantCode == CodeChecksum && res.SHA256 != sum(flipped) {
				t.Error("Result.SHA256 must carry the actual digest")
			}
		})
	}
}

func TestFetchComputedChecksum(t *testing.T) {
	data := genData(testSize, 7)
	e := newEnv(t, nil)
	srv := newTestServer(t, data)
	j := e.job(srv.URL+"/f", data)
	j.ExpectedSHA256, j.ExpectedSize = "", 0
	res := mustFetch(t, e, j)
	if res.SHA256 != sum(data) || res.Bytes != testSize {
		t.Errorf("Result = %+v: with nothing pinned the digest is returned for the caller to record", res)
	}
}

func TestFetchStallAbortsAndResumes(t *testing.T) {
	data := genData(testSize, 8)
	e := newEnv(t, func(o *Options) {})
	e.clk.auto = true // backoff waits are instant; the stall timer still fires only on advance
	srv := newTestServer(t, data)
	const sent = 4 * testBuf
	srv.handler = func(n int, w http.ResponseWriter, r *http.Request) bool {
		if n > 0 {
			return false
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data[:sent])
		w.(http.Flusher).Flush()
		<-r.Context().Done() // the client gives up
		return true
	}
	type out struct {
		res Result
		err error
	}
	done := make(chan out, 1)
	go func() {
		res, err := e.dl.Fetch(context.Background(), e.job(srv.URL+"/f", data), e.cp)
		done <- out{res, err}
	}()
	// The client has applied the four blocks and is waiting in a Read with its watchdog armed.
	waitFor(t, "the client to apply the data and wait for more", func() bool { return e.fs.total() == sent && e.clk.armed() == 1 })
	e.clk.advance(StallTimeout - time.Second)
	if e.clk.armed() != 1 {
		t.Fatal("the watchdog fired 1 s early")
	}
	e.clk.advance(time.Second)
	r := <-done
	if r.err != nil {
		t.Fatalf("Fetch: %v\n%s", r.err, e.logs.String())
	}
	reqs := srv.requests()
	if len(reqs) != 2 || reqs[1].Range != "bytes="+strconv.Itoa(sent)+"-" {
		t.Fatalf("requests = %+v, want a resume at %d", reqs, sent)
	}
	if string(e.readPart()) != string(data) || r.res.Attempts != 2 {
		t.Errorf("Result = %+v", r.res)
	}
}

// Time spent waiting for the bucket is not a stall: the watchdog is armed only around reads.
func TestFetchSlowBucketIsNotAStall(t *testing.T) {
	data := genData(testSize, 9)
	e := newEnv(t, nil)
	e.bucket.hook = func(int64) { e.clk.advance(10 * StallTimeout) } // a very slow link: the bucket "sleeps"
	srv := newTestServer(t, data)
	res := mustFetch(t, e, e.job(srv.URL+"/f", data))
	if res.Attempts != 1 || srv.count() != 1 {
		t.Errorf("attempts = %d, requests = %d: a throttled download was aborted as stalled", res.Attempts, srv.count())
	}
}

func TestFetchCancelStopsAtABlockAndResumesExactly(t *testing.T) {
	data := genData(testSize, 10)
	e := newEnv(t, nil)
	srv := newTestServer(t, data)
	ctx, cancel := context.WithCancel(context.Background())
	e.bucket.hook = func(total int64) {
		if total >= 5*testBuf {
			cancel()
		}
	}
	res, err := e.dl.Fetch(ctx, e.job(srv.URL+"/f", data), e.cp)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if res.Bytes != 5*testBuf {
		t.Errorf("Bytes = %d, want a stop at the block boundary %d", res.Bytes, 5*testBuf)
	}
	cp, ok := e.cp.Get(testName)
	if !ok || cp.Offset != 5*testBuf {
		t.Fatalf("checkpoint on cancel = %+v (present %v), want offset %d", cp, ok, 5*testBuf)
	}
	if got := e.readPart(); len(got) != 5*testBuf || string(got) != string(data[:5*testBuf]) {
		t.Errorf(".part has %d bytes", len(got))
	}
	e.bucket.hook = nil
	res2 := mustFetch(t, e, e.job(srv.URL+"/f", data))
	reqs := srv.requests()
	if res2.ResumedFrom != 5*testBuf || reqs[len(reqs)-1].Range != "bytes=20480-" {
		t.Errorf("resume: %+v, last request %+v", res2, reqs[len(reqs)-1])
	}
	if string(e.readPart()) != string(data) {
		t.Error("the resumed file differs")
	}
}

func TestFetchCancelledBeforeStart(t *testing.T) {
	data := genData(1000, 11)
	e := newEnv(t, nil)
	srv := newTestServer(t, data)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.dl.Fetch(ctx, e.job(srv.URL+"/f", data), e.cp); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if srv.count() != 0 {
		t.Error("a request was made after cancel")
	}
}

func TestFetchCheckpointCadence(t *testing.T) {
	data := genData(testSize, 12)
	e := newEnv(t, nil)
	srv := newTestServer(t, data)
	mustFetch(t, e, e.job(srv.URL+"/f", data))
	// 200 KiB with a 32 KiB step: checkpoints at 32, 64, ... 192 KiB, plus the final one.
	if want := testSize/testCkpt + 1; e.cp.Saves != want {
		t.Errorf("checkpoints saved = %d, want %d", e.cp.Saves, want)
	}
}

func TestFetchCommitRenamesOnlyAfterVerification(t *testing.T) {
	data := genData(testSize, 13)
	e := newEnv(t, nil)
	srv := newTestServer(t, data)
	j := e.job(srv.URL+"/f", data)
	j.Commit = true
	res := mustFetch(t, e, j)
	if res.Final != e.finalPath() {
		t.Errorf("Final = %q, want %q", res.Final, e.finalPath())
	}
	if _, err := os.Stat(e.partPath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the .part must be gone after a commit: %v", err)
	}
	if b, _ := os.ReadFile(e.finalPath()); string(b) != string(data) {
		t.Error("the committed file differs")
	}
	if _, ok := e.cp.Get(testName); ok {
		t.Error("the checkpoint of a committed file must be cleared")
	}

	// A mismatch is never committed.
	e2 := newEnv(t, nil)
	bad := append([]byte(nil), data...)
	bad[5] ^= 1
	srv2 := newTestServer(t, bad)
	j2 := e2.job(srv2.URL+"/f", data)
	j2.Commit = true
	if _, err := e2.dl.Fetch(context.Background(), j2, e2.cp); !errors.Is(err, ErrMismatch) {
		t.Fatalf("err = %v", err)
	}
	if len(e2.fs.renames) != 0 {
		t.Errorf("renamed %v after a mismatch", e2.fs.renames)
	}
	if _, err := os.Stat(e2.finalPath()); !errors.Is(err, os.ErrNotExist) {
		t.Error("a final file exists after a mismatch")
	}
}

func TestExplicitCommit(t *testing.T) {
	data := genData(testSize, 14)
	e := newEnv(t, nil)
	srv := newTestServer(t, data)
	j := e.job(srv.URL+"/f", data)
	res := mustFetch(t, e, j)
	if res.Final != "" {
		t.Fatal("Fetch without Commit must leave the .part")
	}
	final, err := e.dl.Commit(j, e.cp)
	if err != nil || final != e.finalPath() {
		t.Fatalf("Commit = %q, %v", final, err)
	}
	if b, _ := os.ReadFile(final); string(b) != string(data) {
		t.Error("committed file differs")
	}
}

func TestFetchRejectsBadJobs(t *testing.T) {
	e := newEnv(t, nil)
	good := FileJob{RootID: "internal", Root: e.root, Item: "i", Version: "v", Name: "a.bin", URL: "https://files.example.test/a.bin"}
	for name, mod := range map[string]func(*FileJob){
		"no root":          func(j *FileJob) { j.Root = "" },
		"item climbs":      func(j *FileJob) { j.Item = ".." },
		"version has a /":  func(j *FileJob) { j.Version = "a/b" },
		"name climbs":      func(j *FileJob) { j.Name = "../x" },
		"name is absolute": func(j *FileJob) { j.Name = "/x" },
		"name is MANIFEST": func(j *FileJob) { j.Name = "MANIFEST.json" },
		"negative size":    func(j *FileJob) { j.ExpectedSize = -1 },
		"short sha":        func(j *FileJob) { j.ExpectedSHA256 = "abcd" },
		"non-hex sha":      func(j *FileJob) { j.ExpectedSHA256 = strings.Repeat("z", 64) },
		"ftp":              func(j *FileJob) { j.URL = "ftp://files.example.test/a" },
		"credentials":      func(j *FileJob) { j.URL = "https://user:pw@files.example.test/a" },
		"no host":          func(j *FileJob) { j.URL = "https:///a" },
	} {
		t.Run(name, func(t *testing.T) {
			j := good
			mod(&j)
			if _, err := e.dl.Fetch(context.Background(), j, nil); !errors.Is(err, ErrInvalidJob) {
				t.Errorf("err = %v, want ErrInvalidJob", err)
			}
		})
	}
}

func TestRequestHeaders(t *testing.T) {
	data := genData(5000, 15)
	e := newEnv(t, nil)
	srv := newTestServer(t, data)
	mustFetch(t, e, e.job(srv.URL+"/f", data))
	r := srv.requests()[0]
	if want := "localai-agent/9.9.9 (+https://github.com/marinoscar/local-ai)"; r.UA != want {
		t.Errorf("User-Agent = %q, want %q", r.UA, want)
	}
	if r.AcceptEnc != "identity" {
		t.Errorf("Accept-Encoding = %q: Range offsets count wire bytes, so compression must stay off", r.AcceptEnc)
	}
	if r.Auth != "" {
		t.Error("an Authorization header was sent for a job without Auth")
	}
	tr := e.opts.HTTP.c.Transport.(*http.Transport)
	if tr.Proxy != nil {
		t.Error("the transport must not use a proxy (no proxy environment)")
	}
	if tr.TLSClientConfig.MinVersion < 0x0303 || tr.TLSClientConfig.InsecureSkipVerify {
		t.Errorf("TLS config: %+v", tr.TLSClientConfig)
	}
	if tr.TLSHandshakeTimeout != 15*time.Second || tr.ResponseHeaderTimeout != 30*time.Second {
		t.Error("timeouts are not 15 s and 30 s")
	}
}

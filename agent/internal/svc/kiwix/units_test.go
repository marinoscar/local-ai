package kiwix

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

// fakeUnits records the calls and answers State from a field.
type fakeUnits struct {
	mu    sync.Mutex
	state services.UnitState
	calls []string
	err   error
}

func (f *fakeUnits) State(context.Context, string) (services.UnitState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, f.err
}
func (f *fakeUnits) rec(v string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, v)
	return nil
}
func (f *fakeUnits) Start(context.Context, string) error   { return f.rec("start") }
func (f *fakeUnits) Stop(context.Context, string) error    { return f.rec("stop") }
func (f *fakeUnits) Restart(context.Context, string) error { return f.rec("restart") }
func (f *fakeUnits) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// scriptedRegen returns the next Result of a list on every Regenerate.
type scriptedRegen struct {
	results []Result
	i       int
}

func (s *scriptedRegen) Regenerate(context.Context) (Result, error) {
	r := s.results[s.i]
	if s.i < len(s.results)-1 {
		s.i++
	}
	return r, nil
}
func (s *scriptedRegen) RegenerateRoot(context.Context, string) (RootResult, error) {
	return RootResult{}, nil
}

func unit(active string) services.UnitState {
	return services.UnitState{Unit: services.UnitKiwix, ActiveState: active}
}

func TestEmptyStopsAndBooksStart(t *testing.T) {
	empty, full := Result{Empty: true}, Result{Books: 2}
	tests := []struct {
		name    string
		runs    []Result
		state   string
		want    []string
		wantAll [][]string // calls after each Once
	}{
		{"empty and running: stop", []Result{empty}, services.ActiveActive, []string{"stop"}, nil},
		{"empty and already stopped: nothing", []Result{empty}, services.ActiveInactive, nil, nil},
		{"first run with books and a stopped unit: start", []Result{full}, services.ActiveInactive, []string{"start"}, nil},
		{"first run with books and a running unit: nothing", []Result{full}, services.ActiveActive, nil, nil},
		{"failed unit is the supervisor's: nothing", []Result{full}, services.ActiveFailed, nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u := &fakeUnits{state: unit(tc.state)}
			s := NewService(&scriptedRegen{results: tc.runs})
			s.Units = u
			s.Once(context.Background())
			if got := u.got(); len(got) != len(tc.want) || (len(got) > 0 && got[0] != tc.want[0]) {
				t.Fatalf("calls %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEmptyThenNonEmptyStopsThenStarts(t *testing.T) {
	u := &fakeUnits{state: unit(services.ActiveActive)}
	s := NewService(&scriptedRegen{results: []Result{{Books: 1}, {Empty: true}, {Books: 1}}})
	s.Units = u
	s.Once(context.Background()) // running with books: nothing
	s.Once(context.Background()) // empty: stop
	u.mu.Lock()
	u.state = unit(services.ActiveInactive)
	u.mu.Unlock()
	s.Once(context.Background()) // books again: start
	if got := u.got(); len(got) != 2 || got[0] != "stop" || got[1] != "start" {
		t.Fatalf("calls %v", got)
	}
	u.mu.Lock()
	u.state = unit(services.ActiveInactive) // the operator stops it by hand
	u.mu.Unlock()
	s.Once(context.Background())
	if got := u.got(); len(got) != 2 {
		t.Fatalf("a unit stopped on purpose with books must be left alone, calls %v", got)
	}
}

type fakeFD struct {
	links []string
	err   error
	pids  []int
}

func (f *fakeFD) Links(pid int) ([]string, error) {
	f.pids = append(f.pids, pid)
	return f.links, f.err
}

func TestOpenFileCheckAfterUnmount(t *testing.T) {
	tests := []struct {
		name        string
		links       []string
		pid         int
		wantRestart bool
	}{
		{"handle under the removed mount", []string{"/dev/null", "/media/x/localai/kiwix/a/a.zim"}, 42, true},
		{"deleted suffix still counts", []string{"/media/x/localai/kiwix/a/a.zim (deleted)"}, 42, true},
		{"only other paths", []string{"/var/lib/localai/kiwix/library.xml", "/media/x/localai2/kiwix/a.zim"}, 42, false},
		{"no process", []string{"/media/x/localai/kiwix/a.zim"}, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u := &fakeUnits{state: services.UnitState{ActiveState: services.ActiveActive, MainPID: tc.pid}}
			fd := &fakeFD{links: tc.links}
			var waited []time.Duration
			s := NewService(&scriptedRegen{results: []Result{{Books: 1}}})
			s.Units, s.FD = u, fd
			s.Sleep = func(_ context.Context, d time.Duration) error { waited = append(waited, d); return nil }
			root := ark.Root{ID: "ark-0000aaaa", Path: "/media/x/localai"}
			s.Roots = func() []ark.Root { return []ark.Root{root} }
			s.Once(context.Background()) // the root is seen mounted
			s.Roots = func() []ark.Root { return nil }
			s.Removed("ark-0000aaaa")
			s.Once(context.Background())
			restarted := false
			for _, c := range u.got() {
				restarted = restarted || c == "restart"
			}
			if restarted != tc.wantRestart {
				t.Fatalf("restarted %v, want %v (calls %v)", restarted, tc.wantRestart, u.got())
			}
			if len(waited) != 1 || waited[0] != DefaultOpenFileWait {
				t.Fatalf("waited %v, want one wait of 10 s after the rename", waited)
			}
		})
	}
}

func TestNoOpenFileCheckWithoutUnmount(t *testing.T) {
	u := &fakeUnits{state: services.UnitState{ActiveState: services.ActiveActive, MainPID: 7}}
	fd := &fakeFD{}
	s := NewService(&scriptedRegen{results: []Result{{Books: 1}}})
	s.Units, s.FD = u, fd
	s.Once(context.Background())
	if len(fd.pids) != 0 {
		t.Fatal("the fd tree is only scanned after an unmount")
	}
}

func TestProcFDReadsLinks(t *testing.T) {
	root := t.TempDir()
	fdDir := filepath.Join(root, strconv.Itoa(99), "fd")
	if err := os.MkdirAll(fdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/media/x/localai/kiwix/a.zim", filepath.Join(fdDir, "3")); err != nil {
		t.Fatal(err)
	}
	links, err := ProcFD{Root: root}.Links(99)
	if err != nil || !holdsUnder(links, "/media/x/localai") || holdsUnder(links, "/media/x/loc") {
		t.Fatalf("links %v err %v", links, err)
	}
	if _, err := (ProcFD{Root: root}).Links(100); err == nil {
		t.Fatal("a missing process must be an error")
	}
}

type fakeFetch struct {
	body []byte
	err  error
	url  string
}

func (f *fakeFetch) Get(_ context.Context, u string) ([]byte, error) { f.url = u; return f.body, f.err }

func TestStatusContentAndReadiness(t *testing.T) {
	opds := func(n string) []byte {
		return []byte(`<feed xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/"><opensearch:totalResults>` + n + `</opensearch:totalResults></feed>`)
	}
	books := []BookRef{{ContentID: "wikimed-en", Name: "mdwiki_en_all", Root: "internal"}, {ContentID: "water", Name: "water_en", Root: "ark-0000aaaa"}}
	tests := []struct {
		name         string
		set          []BookRef
		neverSet     bool
		fetch        *fakeFetch
		wantMissing  bool
		wantDetail   string
		wantProblems int
	}{
		{"before the first run claims nothing", nil, true, &fakeFetch{body: opds("0")}, false, "0 books", 0},
		{"empty library is missing content", []BookRef{}, false, &fakeFetch{err: errors.New("down")}, true, "0 books", 0},
		{"totalResults equals the books", books, false, &fakeFetch{body: opds("2")}, false, "2 books", 0},
		{"totalResults lower: a ZIM was skipped", books, false, &fakeFetch{body: opds("1")}, false, "2 books", 1},
		{"unreadable catalog is no finding", books, false, &fakeFetch{body: []byte("<x/>")}, false, "2 books", 0},
		{"one book", books[:1], false, &fakeFetch{body: opds("1")}, false, "1 book", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := NewStatus()
			if !tc.neverSet {
				st.Set(tc.set)
			}
			p := &health.FuncProbe{}
			st.Attach(p, tc.fetch, func() int { return 8888 })
			if got := p.Content(context.Background()); got.Missing != tc.wantMissing || (got.Missing && got.Code != "LAI-SVC-001") {
				t.Fatalf("content %+v", got)
			}
			rd := p.Readiness(context.Background())
			if rd.Detail != tc.wantDetail || len(rd.Problems) != tc.wantProblems {
				t.Fatalf("readiness %+v", rd)
			}
			if tc.wantProblems == 1 && rd.Problems[0].Code != "LAI-SVC-004" {
				t.Fatalf("problem %+v", rd.Problems)
			}
			if tc.fetch.url != "http://127.0.0.1:8888/library/catalog/v2/entries?count=1" {
				t.Fatalf("url %q", tc.fetch.url)
			}
		})
	}
}

func TestRegenerateFillsStatus(t *testing.T) {
	r := newRig(t)
	r.addZIM(0, "survival-first-aid", "medicine_en", "medicine_en_2024-08.zim", "2024-08")
	r.addZIM(1, "survival-damaged", "bad_en", "damaged_2024-08.zim", "2024-08")
	r.lib.Status = NewStatus()
	res, err := r.lib.Regenerate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got, known := r.lib.Status.Snapshot()
	if !known || len(got) != 1 || got[0].ContentID != "survival-first-aid" || got[0].Root != "internal" || got[0].Name != "medicine_en" || len(res.List) != 1 {
		t.Fatalf("status %+v known %v", got, known)
	}
}

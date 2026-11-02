package state_test

import (
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/state/fake"
)

func stateRenames(m *fake.MemFS) int {
	n := 0
	for _, o := range m.Ops() {
		if o.Kind == fake.KindRename && o.Path == testPath {
			n++
		}
	}
	return n
}

// Run with -race (scripts/agent-ci.sh does): concurrent mutations are serialised without loss and
// readers always see a consistent copy.
func TestConcurrentMutateAndSnapshot(t *testing.T) {
	const writers, perWriter, readers = 8, 40, 4
	m := fake.New()
	s := open(t, m)

	stop := make(chan struct{})
	var rwg sync.WaitGroup
	for r := 0; r < readers; r++ {
		rwg.Add(1)
		go func() {
			defer rwg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				snap := s.Snapshot()
				// Scribble on the copy: with a shared map or slice the race detector or the final count catches it.
				snap.Models.LastUsed["reader"] = time.Now()
				snap.Models.DesiredLoaded = append(snap.Models.DesiredLoaded, "reader")
				if len(snap.Models.DesiredLoaded) > writers*perWriter+1 {
					t.Error("a snapshot holds more entries than were ever added")
					return
				}
				_ = s.Findings()
				_ = s.Writable()
			}
		}()
	}

	var wwg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wwg.Add(1)
		go func(w int) {
			defer wwg.Done()
			for i := 0; i < perWriter; i++ {
				id := strconv.Itoa(w) + "-" + strconv.Itoa(i)
				mutate := s.Mutate
				if i%7 == 0 {
					mutate = s.MutateSync
				}
				err := mutate(func(st *state.State) error {
					st.Models.DesiredLoaded = append(st.Models.DesiredLoaded, id)
					st.Models.LastUsed[id] = time.Unix(int64(i), 0).UTC()
					return nil
				})
				if err != nil {
					t.Error(err)
					return
				}
			}
		}(w)
	}
	wwg.Wait()
	close(stop)
	rwg.Wait()

	snap := s.Snapshot()
	if got := len(snap.Models.DesiredLoaded); got != writers*perWriter {
		t.Fatalf("entries = %d, want %d: a mutation was lost", got, writers*perWriter)
	}
	if got := len(snap.Models.LastUsed); got != writers*perWriter {
		t.Fatalf("last_used entries = %d, want %d", got, writers*perWriter)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	disk := onDisk(t, m, testPath)
	if len(disk.Models.DesiredLoaded) != writers*perWriter {
		t.Fatalf("on disk: %d entries, want %d", len(disk.Models.DesiredLoaded), writers*perWriter)
	}
	if n := stateRenames(m); n < 1 || n > writers*perWriter {
		t.Fatalf("state.json was replaced %d times", n)
	}
	if left := tempNames(m); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

// Mutations queued behind a write in flight share one write, but a sync mutation ends its batch:
// it is never merged with what arrived after it (agent.md section 4.2.2, implementation note 4).
func TestGroupCommitAndSyncBoundary(t *testing.T) {
	m := fake.New()
	s := open(t, m)

	entered := make(chan struct{})
	release := make(chan struct{})
	var once atomic.Bool
	m.Hook = func(op fake.Op) error {
		if op.Kind == fake.KindOpen && once.CompareAndSwap(false, true) {
			close(entered)
			<-release // hold the first write in flight
		}
		return nil
	}

	var wg sync.WaitGroup
	submit := func(name string, mutate func(func(*state.State) error) error) {
		wg.Add(1)
		before := state.Pending(s)
		go func() {
			defer wg.Done()
			err := mutate(func(st *state.State) error {
				st.Models.DesiredLoaded = append(st.Models.DesiredLoaded, name)
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
		for state.Pending(s) == before { // wait until it is queued, so the order is known
			time.Sleep(time.Millisecond)
		}
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = s.Mutate(func(st *state.State) error {
			st.Models.DesiredLoaded = append(st.Models.DesiredLoaded, "first")
			return nil
		})
	}()
	<-entered
	submit("m2", s.Mutate)
	submit("m3", s.Mutate)
	submit("m4-sync", s.MutateSync)
	submit("m5", s.Mutate)
	close(release)
	wg.Wait()

	if n := stateRenames(m); n != 3 {
		t.Fatalf("state.json written %d times, want 3: [first] then [m2 m3 m4-sync] then [m5]", n)
	}
	got := s.Snapshot().Models.DesiredLoaded
	want := []string{"first", "m2", "m3", "m4-sync", "m5"}
	if len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v (mutations apply in arrival order)", got, want)
		}
	}
}

func TestFindingsAreCopies(t *testing.T) {
	m := seeded(map[string][]byte{testPath: fixture(t, "state-corrupt-truncated.json")})
	s := open(t, m)
	f := s.Findings()
	f[0].Details["source"] = "tampered"
	f[0].Message = "tampered"
	if again := s.Findings(); again[0].Details["source"] != "defaults" || again[0].Message == "tampered" {
		t.Fatalf("Findings leaked its internals: %+v", again[0])
	}
}

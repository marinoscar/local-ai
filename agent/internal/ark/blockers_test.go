package ark_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
)

// fakeProc is a /proc the tests build: processes with a name and the targets of their fd and cwd links.
type fakeProc struct {
	mu      sync.Mutex
	procs   map[int]*fakeProcess
	pidsErr error
	walks   int
	// onWalk, when set, runs at the start of every walk (every PIDs call) with the walk number from 1.
	onWalk func(n int)
}

type fakeProcess struct {
	comm     string
	links    []string
	linksErr error
}

func newFakeProc() *fakeProc { return &fakeProc{procs: map[int]*fakeProcess{}} }

func (f *fakeProc) add(pid int, comm string, links ...string) *fakeProc {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.procs[pid] = &fakeProcess{comm: comm, links: links}
	return f
}

func (f *fakeProc) drop(pid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.procs, pid)
}

func (f *fakeProc) PIDs() ([]int, error) {
	f.mu.Lock()
	f.walks++
	n, hook := f.walks, f.onWalk
	f.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pidsErr != nil {
		return nil, f.pidsErr
	}
	var out []int
	for pid := range f.procs {
		out = append(out, pid)
	}
	return out, nil
}

func (f *fakeProc) FDLinks(pid int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.procs[pid]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]string(nil), p.links...), p.linksErr
}

func (f *fakeProc) Comm(pid int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.procs[pid]
	if !ok {
		return "", os.ErrNotExist
	}
	return p.comm, nil
}

const mnt = "/media/operator/AI-ARK"

func TestUnderMount(t *testing.T) {
	for _, tc := range []struct {
		name, mount, target string
		want                bool
	}{
		{"the mount point itself (a shell's cwd)", mnt, mnt, true},
		{"a file below it", mnt, mnt + "/localai/models/m.gguf", true},
		{"a deleted file still open", mnt, mnt + "/localai/models/m.gguf (deleted)", true},
		{"a path with dots is cleaned", mnt, mnt + "/localai/../localai/x", true},
		{"a sibling whose name starts the same", mnt, mnt + "2/file", false},
		{"the parent directory", mnt, "/media/operator", false},
		{"another mount", mnt, "/srv/localai/models/x", false},
		{"a socket", mnt, "socket:[1234]", false},
		{"an anonymous inode", mnt, "anon_inode:[eventfd]", false},
		{"a pipe", mnt, "pipe:[99]", false},
		{"the root is never a drive", "/", "/etc/passwd", false},
		{"an empty mount", "", "/etc/passwd", false},
		{"a relative mount", "media", "media/x", false},
		{"a trailing slash on the mount", mnt + "/", mnt + "/x", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ark.UnderMount(tc.mount, tc.target); got != tc.want {
				t.Errorf("UnderMount(%q, %q) = %v, want %v", tc.mount, tc.target, got, tc.want)
			}
		})
	}
}

func TestFindBlockers(t *testing.T) {
	const self = 100
	for _, tc := range []struct {
		name  string
		build func(*fakeProc)
		want  []ark.Blocker
	}{
		{name: "nothing holds the drive", build: func(p *fakeProc) {
			p.add(1, "systemd", "/", "/dev/null").add(self, "aiagent", "/", "/var/lib/localai/state.db")
		}},
		{name: "a shell with its cwd on the drive is a process blocker", build: func(p *fakeProc) {
			p.add(4242, "bash", mnt+"/localai", "/dev/pts/3")
		}, want: []ark.Blocker{{Kind: ark.BlockerProcess, Name: "bash", PID: 4242}}},
		{name: "a file open on the drive", build: func(p *fakeProc) {
			p.add(300, "llama-server", "/srv/llama", mnt+"/localai/models/m.gguf")
		}, want: []ark.Blocker{{Kind: ark.BlockerProcess, Name: "llama-server", PID: 300}}},
		{name: "one blocker per process however many files", build: func(p *fakeProc) {
			p.add(300, "kiwix-serve", mnt+"/localai/kiwix/a.zim", mnt+"/localai/kiwix/b.zim", mnt+"/localai")
		}, want: []ark.Blocker{{Kind: ark.BlockerProcess, Name: "kiwix-serve", PID: 300}}},
		{name: "the agent's own hold is the sync kind, whatever its name", build: func(p *fakeProc) {
			p.add(self, "aiagent", mnt+"/localai/.staging/m.part")
		}, want: []ark.Blocker{{Kind: ark.BlockerSync, Name: "sync", PID: self}}},
		{name: "sorted by pid", build: func(p *fakeProc) {
			p.add(900, "vim", mnt+"/localai/notes").add(50, "bash", mnt).add(self, "aiagent", mnt+"/x")
		}, want: []ark.Blocker{
			{Kind: ark.BlockerProcess, Name: "bash", PID: 50},
			{Kind: ark.BlockerSync, Name: "sync", PID: self},
			{Kind: ark.BlockerProcess, Name: "vim", PID: 900},
		}},
		{name: "a process that vanished or cannot be read is skipped", build: func(p *fakeProc) {
			p.add(7, "ghost", mnt+"/x")
			p.procs[7].linksErr = os.ErrPermission
			p.add(8, "bash", mnt+"/x")
		}, want: []ark.Blocker{{Kind: ark.BlockerProcess, Name: "bash", PID: 8}}},
		{name: "a name that cannot be read falls back to the pid", build: func(p *fakeProc) {
			p.add(9, "", mnt+"/x")
		}, want: []ark.Blocker{{Kind: ark.BlockerProcess, Name: "pid 9", PID: 9}}},
		{name: "a sibling mount is not this drive", build: func(p *fakeProc) {
			p.add(10, "bash", mnt+"2/localai")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newFakeProc()
			tc.build(p)
			got, err := ark.FindBlockers(p, mnt, self)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("blockers = %+v, want %+v", got, tc.want)
			}
		})
	}

	t.Run("the list is bounded", func(t *testing.T) {
		p := newFakeProc()
		for pid := 1; pid <= 200; pid++ {
			p.add(pid+1000, fmt.Sprintf("worker%d", pid), mnt+"/x")
		}
		got, err := ark.FindBlockers(p, mnt, self)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 32 || got[0].PID != 1001 {
			t.Errorf("got %d blockers, first %+v", len(got), got[0])
		}
	})

	t.Run("an unreadable /proc is an error", func(t *testing.T) {
		p := newFakeProc()
		p.pidsErr = errors.New("no /proc")
		if _, err := ark.FindBlockers(p, mnt, self); err == nil {
			t.Fatal("want an error")
		}
	})
}

// TestOSProcReadsARealTree runs the real ProcFS over a directory built like /proc: numeric directories
// with cwd and fd links and a comm file, a non-numeric entry, and a process whose fd directory is missing.
func TestOSProcReadsARealTree(t *testing.T) {
	tmp := t.TempDir()
	drive := filepath.Join(tmp, "media", "AI-ARK")
	if err := os.MkdirAll(filepath.Join(drive, "localai"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(drive, "localai", "m.gguf")
	writeFile(t, file, "x")
	proc := filepath.Join(tmp, "proc")

	mk := func(pid, comm, cwd string, fds ...string) {
		dir := filepath.Join(proc, pid)
		if err := os.MkdirAll(filepath.Join(dir, "fd"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "comm"), comm+"\n")
		if err := os.Symlink(cwd, filepath.Join(dir, "cwd")); err != nil {
			t.Fatal(err)
		}
		for i, target := range fds {
			if err := os.Symlink(target, filepath.Join(dir, "fd", fmt.Sprint(i))); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("11", "bash", drive+"/localai")
	mk("12", "llama-server", "/", file, "socket:[5]")
	mk("13", "cron", "/", "/dev/null")
	if err := os.MkdirAll(filepath.Join(proc, "self"), 0o755); err != nil { // not a pid
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(proc, "14"), 0o755); err != nil { // no cwd, no fd: a kernel thread
		t.Fatal(err)
	}

	got, err := ark.FindBlockers(ark.OSProc{Root: proc}, drive, 99)
	if err != nil {
		t.Fatal(err)
	}
	want := []ark.Blocker{
		{Kind: ark.BlockerProcess, Name: "bash", PID: 11},
		{Kind: ark.BlockerProcess, Name: "llama-server", PID: 12},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("blockers = %+v, want %+v", got, want)
	}
	if _, err := (ark.OSProc{Root: filepath.Join(tmp, "missing")}).PIDs(); err == nil {
		t.Error("a missing /proc must be an error")
	}
}

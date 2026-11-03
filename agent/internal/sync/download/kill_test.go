package download

import (
	"context"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
)

// openJournal opens (or reopens, after a "crash") the journal in dir, makes sure the item has a
// downloading record at the test version, and returns the checkpointer over it.
func openJournal(t *testing.T, dir string) (*journal.Journal, *JournalCheckpointer) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	j, _, err := journal.Open(context.Background(), journal.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	run, err := j.Runs.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })
	if _, err := j.Transition(run, testRoot, testItem, testVer, journal.StateDownloading, nil); err != nil {
		t.Fatal(err)
	}
	return j, &JournalCheckpointer{Items: j.Items, Run: run, RootID: testRoot, ItemID: testItem, Version: testVer}
}

// Kill the "process" at 50 random byte offsets: the downloader is abandoned in the middle of a write
// (no checkpoint, no cleanup), the journal is reopened from its files, a new downloader resumes, and
// the file ends identical to the source. The resume never re-downloads a byte that was checkpointed.
func TestKillAtRandomPointsResumesWithoutRedownloadingVerifiedBytes(t *testing.T) {
	data := genData(testSize, 30)
	rnd := rand.New(rand.NewPCG(7, 11))
	for i := 0; i < 50; i++ {
		killAt := int64(1 + rnd.IntN(testSize-1))
		t.Run(strconv.Itoa(i)+"_at_"+itoaN(killAt), func(t *testing.T) {
			e := newEnv(t, nil)
			srv := newTestServer(t, data)
			jdir := filepath.Join(t.TempDir(), "journal")
			_, cp := openJournal(t, jdir)
			e.fs.setKill(killAt)
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done() // runs on Goexit: the "process" is gone, nothing else is cleaned up
				_, _ = e.dl.Fetch(context.Background(), e.job(srv.URL+"/f", data), cp)
			}()
			wg.Wait()
			if e.fs.total() != killAt {
				t.Fatalf("the process was killed after %d bytes, want %d", e.fs.total(), killAt)
			}
			// Restart: a new journal from the files, a new downloader on the same root.
			e.fs.setKill(-1)
			e.rebuild()
			_, cp2 := openJournal(t, jdir)
			res, err := e.dl.Fetch(context.Background(), e.job(srv.URL+"/f", data), cp2)
			if err != nil {
				t.Fatalf("resume: %v\n%s", err, e.logs.String())
			}
			if res.ResumedFrom%testCkpt != 0 || res.ResumedFrom > killAt || killAt-res.ResumedFrom > testCkpt+testBuf {
				t.Errorf("resumed from %d after a kill at %d: not the last checkpoint", res.ResumedFrom, killAt)
			}
			reqs := srv.requests()
			last := reqs[len(reqs)-1]
			if res.ResumedFrom > 0 && last.Range != "bytes="+itoaN(res.ResumedFrom)+"-" {
				t.Errorf("Range = %q, want a resume at %d", last.Range, res.ResumedFrom)
			}
			if res.ResumedFrom == 0 && last.Range != "" {
				t.Errorf("Range = %q for a restart", last.Range)
			}
			if got := e.readPart(); string(got) != string(data) {
				t.Fatalf("the resumed file differs (%d bytes)", len(got))
			}
			if res.SHA256 != sum(data) {
				t.Error("digest differs")
			}
		})
	}
}

const (
	envHelper = "LAI_DL_HELPER"
	envURL    = "LAI_DL_URL"
	envRoot   = "LAI_DL_ROOT"
	envJDir   = "LAI_DL_JOURNAL"
	envSeed   = "LAI_DL_SEED"
	envSize   = "LAI_DL_SIZE"
)

// TestHelperDownloadProcess is not a test: the SIGKILL test re-executes the test binary into it to
// play a downloader process that the parent kills with SIGKILL.
func TestHelperDownloadProcess(t *testing.T) {
	if os.Getenv(envHelper) != "1" {
		t.Skip("helper process")
	}
	seed, _ := strconv.ParseUint(os.Getenv(envSeed), 10, 64)
	size, _ := strconv.Atoi(os.Getenv(envSize))
	data := genData(size, seed)
	_, cp := openJournal(t, os.Getenv(envJDir))
	dl := New(Options{CheckpointEvery: 256 << 10, BufSize: 64 << 10})
	_, err := dl.Fetch(context.Background(), FileJob{
		RootID: testRoot, Root: os.Getenv(envRoot), Item: testItem, Version: testVer, Name: testName,
		URL: os.Getenv(envURL), ExpectedSize: int64(len(data)), ExpectedSHA256: sum(data),
	}, cp)
	if err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

// Real SIGKILL of a real process, with real fsyncs and a journal read back from its files: the
// resumed download ends with identical bytes and a correct hash (acceptance criterion).
func TestSIGKILLResume(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	self, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	const size = 3 << 20
	data := genData(size, 31)
	rnd := rand.New(rand.NewPCG(5, 6))
	killedMidway := 0
	for i := 0; i < 8; i++ {
		root, jdir := t.TempDir(), filepath.Join(t.TempDir(), "journal")
		srv := newTestServer(t, data)
		srv.setHandler(func(n int, w http.ResponseWriter, r *http.Request) bool {
			// A paced server, so a kill lands in the middle of a download.
			start := startOf(r)
			w.Header().Set("ETag", `"v1"`)
			if start > 0 {
				w.Header().Set("Content-Range", "bytes "+strconv.Itoa(start)+"-"+strconv.Itoa(size-1)+"/"+strconv.Itoa(size))
				w.Header().Set("Content-Length", strconv.Itoa(size-start))
				w.WriteHeader(http.StatusPartialContent)
			} else {
				w.Header().Set("Content-Length", strconv.Itoa(size))
			}
			for off := start; off < size; off += 128 << 10 {
				if r.Context().Err() != nil {
					return true
				}
				_, _ = w.Write(data[off:min(off+128<<10, size)])
				w.(http.Flusher).Flush()
				time.Sleep(time.Millisecond)
			}
			return true
		})
		job := FileJob{Root: root, Item: testItem, Version: testVer, Name: testName}
		cmd := exec.Command(self, "-test.run=^TestHelperDownloadProcess$")
		cmd.Env = append(os.Environ(), envHelper+"=1", envURL+"="+srv.URL+"/f", envRoot+"="+root, envJDir+"="+jdir,
			envSeed+"=31", envSize+"="+strconv.Itoa(size))
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		threshold := int64(1 + rnd.IntN(size-1))
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()
		deadline := time.After(30 * time.Second)
	wait:
		for {
			select {
			case <-exited: // finished before the kill: still a valid scenario
				break wait
			case <-deadline:
				_ = cmd.Process.Kill()
				t.Fatal("the helper did not reach the kill point")
			default:
			}
			if st, err := os.Stat(job.PartPath()); err == nil && st.Size() >= threshold {
				_ = cmd.Process.Signal(syscall.SIGKILL)
				<-exited
				killedMidway++
				break wait
			}
			time.Sleep(200 * time.Microsecond)
		}

		// The restart, in this process: journal from its files, a new downloader, the same root.
		_, cp := openJournal(t, jdir)
		dl := New(Options{CheckpointEvery: 256 << 10, BufSize: 64 << 10})
		j := FileJob{
			RootID: testRoot, Root: root, Item: testItem, Version: testVer, Name: testName,
			URL: srv.URL + "/f", ExpectedSize: size, ExpectedSHA256: sum(data),
		}
		res, err := dl.Fetch(context.Background(), j, cp)
		if err != nil {
			t.Fatalf("iteration %d: resume: %v", i, err)
		}
		if res.ResumedFrom%(256<<10) != 0 {
			t.Errorf("iteration %d: resumed from %d, not a checkpoint", i, res.ResumedFrom)
		}
		got, err := os.ReadFile(j.PartPath())
		if err != nil || string(got) != string(data) {
			t.Fatalf("iteration %d: the resumed file differs (%d bytes, %v)", i, len(got), err)
		}
		if res.SHA256 != sum(data) {
			t.Errorf("iteration %d: digest differs", i)
		}
	}
	t.Logf("%d of 8 downloads were killed mid-flight", killedMidway)
}

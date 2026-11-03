package build

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// buildsLeft lists the entries of builds/ in name order.
func buildsLeft(t *testing.T, h *harness) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(h.root, "builds"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func TestRetentionKeepsCurrentPreviousAndNewestOther(t *testing.T) {
	// The situation right after a successful switch: new (current), old (previous), then three
	// other builds, newest first: only the newest of those stays.
	h := newHarness(t, withoutPrevious())
	h.makeBuild("aaaaaaaaa-cuda", testNow.Add(-24*time.Hour))
	h.makeBuild("bbbbbbbbb-cuda", testNow.Add(-72*time.Hour))
	h.makeBuild("ccccccccc-cuda", testNow.Add(-96*time.Hour))
	h.makeBuild("ddddddddd-cuda", testNow.Add(-5*time.Hour)) // the previous build
	h.makeBuild(testID, testNow)
	if err := os.Symlink("builds/"+testID, filepath.Join(h.root, "current")); err != nil {
		t.Fatal(err)
	}
	removed, warns := h.manager().retain(DefaultKeep, Plan{Commit: testSHA}, []string{testID, "ddddddddd-cuda"})
	want := []string{"aaaaaaaaa-cuda", "ddddddddd-cuda", testID}
	if got := buildsLeft(t, h); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("left %v, want %v", got, want)
	}
	if len(removed) != 2 || len(warns) != 0 {
		t.Errorf("removed %v warnings %v", removed, warns)
	}
}

func TestRetentionKeepCount(t *testing.T) {
	tests := []struct {
		keep int
		want []string
	}{
		{1, []string{"cur-cuda", "prev-cuda"}},
		{0, []string{"cur-cuda", "prev-cuda"}}, // below one is one: current and previous are never deleted
		{2, []string{"cur-cuda", "o1-cuda", "prev-cuda"}},
		{3, []string{"cur-cuda", "o1-cuda", "o2-cuda", "prev-cuda"}},
		{9, []string{"cur-cuda", "o1-cuda", "o2-cuda", "o3-cuda", "prev-cuda"}},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprint("keep ", tc.keep), func(t *testing.T) {
			h := newHarness(t, withoutPrevious())
			h.makeBuild("cur-cuda", testNow)
			h.makeBuild("prev-cuda", testNow.Add(-10*24*time.Hour)) // the oldest, yet kept
			h.makeBuild("o1-cuda", testNow.Add(-1*time.Hour))
			h.makeBuild("o2-cuda", testNow.Add(-2*time.Hour))
			h.makeBuild("o3-cuda", testNow.Add(-3*time.Hour))
			if err := os.Symlink("builds/cur-cuda", filepath.Join(h.root, "current")); err != nil {
				t.Fatal(err)
			}
			h.manager().retain(tc.keep, Plan{Commit: testSHA}, []string{"cur-cuda", "prev-cuda"})
			if got := buildsLeft(t, h); fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("left %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRetentionWithoutActivationKeepsTheBuildJustMade(t *testing.T) {
	// current does not change; the state's previous build is kept; the new build is the newest other.
	h := newHarness(t, withoutPrevious())
	h.makeBuild("cur-cuda", testNow.Add(-48*time.Hour))
	h.makeBuild("prev-cuda", testNow.Add(-96*time.Hour))
	h.makeBuild("old-cuda", testNow.Add(-200*time.Hour))
	h.makeBuild("new-cuda", testNow)
	if err := os.Symlink("builds/cur-cuda", filepath.Join(h.root, "current")); err != nil {
		t.Fatal(err)
	}
	h.manager().retain(DefaultKeep, Plan{Commit: testSHA}, []string{"new-cuda", "cur-cuda", "prev-cuda"})
	want := []string{"cur-cuda", "new-cuda", "prev-cuda"}
	if got := buildsLeft(t, h); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("left %v, want %v", got, want)
	}
}

func TestRetentionNeverDeletesWhatCurrentPointsAt(t *testing.T) {
	h := newHarness(t, withoutPrevious())
	h.makeBuild("cur-cuda", testNow.Add(-500*time.Hour)) // the oldest, and in use
	h.makeBuild("a-cuda", testNow.Add(-1*time.Hour))
	h.makeBuild("b-cuda", testNow.Add(-2*time.Hour))
	h.makeBuild("c-cuda", testNow.Add(-3*time.Hour))
	if err := os.Symlink("builds/cur-cuda", filepath.Join(h.root, "current")); err != nil {
		t.Fatal(err)
	}
	h.manager().retain(1, Plan{Commit: testSHA}, nil) // the caller forgot to protect it
	if !h.exists("builds/cur-cuda/BUILD.json") {
		t.Fatal("the build in use was deleted")
	}
}

func TestRetentionCleansLeftoversAndLeavesStrangersAlone(t *testing.T) {
	h := newHarness(t, withoutPrevious())
	h.makeBuild("cur-cuda", testNow)
	h.makeBuild("x-cuda.old-20261101T000000Z", testNow.Add(-24*time.Hour)) // a move-aside is a build
	h.write(filepath.Join(h.root, "builds", "f-cuda.tmp.failed", "build.log"), "log")
	h.write(filepath.Join(h.root, "builds", "g-cuda.tmp", "bin", "x"), "x")
	h.write(filepath.Join(h.root, "builds", "notes", "readme.txt"), "not a build")
	h.write(filepath.Join(h.root, "builds", "stray-file"), "file")
	if err := os.Symlink("builds/cur-cuda", filepath.Join(h.root, "current")); err != nil {
		t.Fatal(err)
	}
	h.manager().retain(DefaultKeep, Plan{Commit: testSHA}, []string{"cur-cuda"})
	want := []string{"cur-cuda", "notes", "stray-file", "x-cuda.old-20261101T000000Z"}
	if got := buildsLeft(t, h); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("left %v, want %v", got, want)
	}
}

func TestRetentionOfSourceTrees(t *testing.T) {
	sha := func(c string) string { return strings.Repeat(c, 40) }
	h := newHarness(t, withoutPrevious(), withoutSource())
	mk := func(name string, ago time.Duration) {
		dir := filepath.Join(h.root, "src", name)
		h.write(filepath.Join(dir, "CMakeLists.txt"), "x")
		at := testNow.Add(-ago)
		if err := os.Chtimes(dir, at, at); err != nil {
			t.Fatal(err)
		}
	}
	mk(sha("a"), 1*time.Hour)
	mk(sha("b"), 2*time.Hour)
	mk(sha("c"), 3*time.Hour)
	mk(sha("d"), 4*time.Hour)
	mk(testSHA, 100*time.Hour) // the commit just built is kept whatever its age
	h.write(filepath.Join(h.root, "src", sha("e")+".tmp", "x"), "x")
	h.write(filepath.Join(h.root, "src", sha("e")+".tmp.tar"), "x")
	h.write(filepath.Join(h.root, "src", "not-a-sha", "x"), "x")
	h.manager().retain(DefaultKeep, Plan{Commit: testSHA}, nil)
	entries, _ := os.ReadDir(filepath.Join(h.root, "src"))
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	sort.Strings(got)
	want := []string{sha("a"), sha("b"), "not-a-sha", testSHA}
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("source trees left %v, want %v", got, want)
	}
}

func TestRetentionFailureIsAWarning(t *testing.T) {
	h := newHarness(t, withoutPrevious())
	h.makeBuild("cur-cuda", testNow)
	h.makeBuild("old1-cuda", testNow.Add(-1*time.Hour))
	h.makeBuild("old2-cuda", testNow.Add(-2*time.Hour))
	h.makeBuild("old3-cuda", testNow.Add(-3*time.Hour))
	if err := os.Symlink("builds/cur-cuda", filepath.Join(h.root, "current")); err != nil {
		t.Fatal(err)
	}
	h.opts.FS = removeFailFS{OSFS: OSFS{}}
	removed, warns := h.manager().retain(1, Plan{Commit: testSHA}, []string{"cur-cuda"})
	if len(removed) != 0 || len(warns) != 2 || !strings.Contains(warns[0], "Could not remove") {
		t.Errorf("removed %v warnings %v", removed, warns)
	}
}

type removeFailFS struct{ OSFS }

func (removeFailFS) RemoveAll(string) error { return os.ErrPermission }

func TestBuildAppliesRetentionAndReportsIt(t *testing.T) {
	h := newHarness(t)
	h.makeBuild("old1-cuda", testNow.Add(-72*time.Hour))
	h.makeBuild("old2-cuda", testNow.Add(-96*time.Hour))
	res, err := h.build()
	if err != nil {
		t.Fatal(err)
	}
	// current = new, previous = the harness's old build, newest other = old1.
	want := []string{oldID, testID, "old1-cuda"}
	if got := buildsLeft(t, h); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("left %v, want %v", got, want)
	}
	if len(res.Removed) != 1 || !strings.HasSuffix(res.Removed[0], "old2-cuda") {
		t.Errorf("removed = %v", res.Removed)
	}
}

func TestFailedBuildSkipsRetention(t *testing.T) {
	h := newHarness(t)
	h.makeBuild("old1-cuda", testNow.Add(-72*time.Hour))
	h.makeBuild("old2-cuda", testNow.Add(-96*time.Hour))
	h.probe.completeErr = fmt.Errorf("boom")
	if _, err := h.build(); err == nil {
		t.Fatal("want a failed check")
	}
	for _, n := range []string{"old1-cuda", "old2-cuda", oldID} {
		if !h.exists("builds/" + n) {
			t.Errorf("%s was deleted after a failed build", n)
		}
	}
}

func TestNoActivationRetentionUsesTheStatePreviousBuild(t *testing.T) {
	h := newHarness(t)
	h.makeBuild("prevstate-cuda", testNow.Add(-300*time.Hour))
	h.makeBuild("other-cuda", testNow.Add(-1*time.Hour))
	h.opts.PreviousBuild = func() string { return "prevstate-cuda" }
	h.req.NoActivate = true
	if _, err := h.build(); err != nil {
		t.Fatal(err)
	}
	want := []string{oldID, testID, "prevstate-cuda"}
	if got := buildsLeft(t, h); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("left %v, want %v", got, want)
	}
}

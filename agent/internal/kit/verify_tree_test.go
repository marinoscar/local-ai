package kit_test

import (
	"os"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/kit/kittest"
)

// Directory items (a SHA256SUMS and a tree hash instead of a file list) and the cheap check of an apt
// closure that is not due for a re-read.

func problemsOf(rep kit.VerifyReport, dir string) []kit.Problem {
	for _, it := range rep.Items {
		if it.Dir == dir {
			return it.Problems
		}
	}
	return nil
}

func hasProblem(list []kit.Problem, path, reason string) bool {
	for _, p := range list {
		if p.Path == path && p.Reason == reason {
			return true
		}
	}
	return false
}

func TestVerifyDirectoryItem(t *testing.T) {
	files := map[string][]byte{"app.deb": []byte("the desktop app\n"), "docs/readme.txt": []byte("read me\n")}
	tests := []struct {
		name   string
		mode   kit.Mode
		do     func(k *kittest.Kit)
		path   string
		reason string
	}{
		{name: "intact", mode: kit.ModeFull, do: func(k *kittest.Kit) {}},
		{name: "a file changed, full", mode: kit.ModeFull, do: func(k *kittest.Kit) { k.Corrupt("desktop/app.deb") },
			path: "desktop/app.deb", reason: kit.ReasonChanged},
		{name: "a file changed, quick cannot see it", mode: kit.ModeQuick, do: func(k *kittest.Kit) { k.Corrupt("desktop/app.deb") }},
		{name: "a file gone, quick sees it", mode: kit.ModeQuick, do: func(k *kittest.Kit) { k.Remove("desktop/docs/readme.txt") },
			path: "desktop/docs/readme.txt", reason: kit.ReasonMissing},
		{name: "SHA256SUMS gone", mode: kit.ModeQuick, do: func(k *kittest.Kit) { k.Remove("desktop/SHA256SUMS") },
			path: "desktop/SHA256SUMS", reason: kit.ReasonNoSums},
		{name: "SHA256SUMS edited to agree with a changed file but not with the manifest", mode: kit.ModeQuick, do: func(k *kittest.Kit) {
			sums := string(k.Read("desktop/SHA256SUMS"))
			line := strings.SplitN(sums, "\n", 2)[0]
			fields := strings.Fields(line)
			k.Write("desktop/SHA256SUMS", []byte(strings.Replace(sums, fields[0], kittest.SHA([]byte("another file")), 1)))
		}, path: "desktop/SHA256SUMS", reason: kit.ReasonSumsMismatch},
		{name: "a link instead of a file", mode: kit.ModeQuick, do: func(k *kittest.Kit) {
			k.Remove("desktop/app.deb")
			if err := os.Symlink(k.Abs("desktop/docs/readme.txt"), k.Abs("desktop/app.deb")); err != nil {
				t.Skip("no symbolic links here")
			}
		}, path: "desktop/app.deb", reason: kit.ReasonNotRegular},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := kittest.New(t)
			k.Tree("desktop", "kit-desktop", files)
			tt.do(k)
			rep := run(t, k, func(o *kit.Options) { o.Mode = tt.mode })
			probs := problemsOf(rep, "desktop")
			if tt.path == "" {
				if len(probs) != 0 || rep.Overall != kit.Green {
					t.Fatalf("problems %+v overall %s", probs, rep.Overall)
				}
				return
			}
			if !hasProblem(probs, tt.path, tt.reason) || rep.Overall != kit.Red || !rep.Damaged() {
				t.Fatalf("problems %+v overall %s (want %s %s)", probs, rep.Overall, tt.path, tt.reason)
			}
			if st, code := partStatus(t, rep, "desktop"); st != kit.StatusDamaged || code != "LAI-KIT-001" {
				t.Errorf("desktop %s %s", st, code)
			}
		})
	}
}

func TestVerifyAptClosureThatIsNotDueIsCheckedByName(t *testing.T) {
	n := kittest.AiN2()
	k := kittest.New(t)
	k.Provision(n)
	quick := func(o *kit.Options) { facts(n)(o); o.Mode = kit.ModeBudgeted }
	if rep := run(t, k, quick); rep.Overall != kit.Green || rep.Totals.FilesHashed != 0 {
		t.Fatalf("overall %s hashed %d", rep.Overall, rep.Totals.FilesHashed)
	}
	k.Remove("debs/resolute-amd64/pool/cmake_3.28.3-1_amd64.deb")
	rep := run(t, k, quick)
	if !hasProblem(problemsOf(rep, "debs/resolute-amd64"), "debs/resolute-amd64/pool/cmake_3.28.3-1_amd64.deb", kit.ReasonMissing) {
		t.Fatalf("problems %+v", problemsOf(rep, "debs/resolute-amd64"))
	}
	// A SHA256SUMS that no longer matches the manifest's tree hash.
	k2 := kittest.New(t)
	k2.Provision(n)
	sums := string(k2.Read("debs/resolute-amd64/SHA256SUMS"))
	k2.Write("debs/resolute-amd64/SHA256SUMS", []byte(strings.Replace(sums, strings.Fields(sums)[0], kittest.SHA([]byte("x")), 1)))
	rep = run(t, k2, quick)
	if !hasProblem(problemsOf(rep, "debs/resolute-amd64"), "debs/resolute-amd64/SHA256SUMS", kit.ReasonSumsMismatch) {
		t.Fatalf("problems %+v", problemsOf(rep, "debs/resolute-amd64"))
	}
}

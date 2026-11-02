package fake

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

func TestFakeAnswersScriptedCommandsAndRejectsOthers(t *testing.T) {
	r := New().Expect(Key("/usr/bin/systemctl", "is-active", "localai-agent"), execx.Result{Stdout: []byte("active\n")}, nil)
	c := execx.Cmd{Path: "/usr/bin/systemctl", Args: []string{"is-active", "localai-agent"}, Timeout: time.Second, MaxOut: 64}
	res, err := r.Run(context.Background(), c)
	if err != nil || string(res.Stdout) != "active\n" {
		t.Fatalf("scripted command: %v %q", err, res.Stdout)
	}
	c.Args = []string{"restart", "localai-agent"}
	if _, err := r.Run(context.Background(), c); err == nil || !strings.Contains(err.Error(), "unexpected command") {
		t.Errorf("unscripted command should fail loudly, got %v", err)
	}
	if n := len(r.Calls()); n != 2 {
		t.Errorf("recorded %d calls, want 2", n)
	}
}

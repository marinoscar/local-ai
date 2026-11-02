package auth

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
)

// TestOverRealListeners proves the whole chain on real sockets: SO_PEERCRED read by the kernel,
// mapped to a principal by the service, and a bearer token over TCP.
func TestOverRealListeners(t *testing.T) {
	e := newEnv(t)
	uid := os.Getuid()
	e.groups.Add(OperatorsGroup, opsGID, uid)

	reg := api.NewRegistry()
	if err := Register(reg, e.svc); err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(probeRoutes()...); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "auth")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "a.sock")
	srv, err := api.New(api.Options{
		Registry: reg, Auth: e.svc, TCPAddr: "127.0.0.1:0", SocketPath: sock,
		Hooks: api.Hooks{LookupGID: func(string) (int, error) { return 0, nil }, Chown: func(string, int, int) error { return nil }},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.SetMutationsOpen(true)
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Stop(ctx)
	})

	unix := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	get := func(c *http.Client, url string, header map[string]string) (int, map[string]any) {
		t.Helper()
		r, _ := http.NewRequest("GET", url, nil)
		for k, v := range header {
			r.Header.Set(k, v)
		}
		resp, err := c.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(b, &out)
		return resp.StatusCode, out
	}

	t.Run("socket: the kernel's peer uid decides", func(t *testing.T) {
		want := "operator:uid=" + strconv.Itoa(uid)
		if uid == 0 {
			want = "admin:uid=0"
		}
		status, body := get(unix, "http://localhost/api/v1/whoami", nil)
		if status != 200 || body["principal"] != want {
			t.Fatalf("%d %v, want %s", status, body, want)
		}
		if uid != 0 { // root is admin whatever the groups say
			e.groups.Remove(OperatorsGroup, uid)
			e.clock.Advance(GroupCacheTTL)
			status, body = get(unix, "http://localhost/api/v1/whoami", nil)
			errBody, _ := body["error"].(map[string]any)
			if status != 403 || errBody["code"] != api.CodePeerNotOperator {
				t.Fatalf("a peer outside the group: %d %v", status, body)
			}
		}
	})
	t.Run("tcp: the bearer token decides", func(t *testing.T) {
		base := "http://" + srv.TCPAddr().String() + "/api/v1/whoami"
		if status, body := get(http.DefaultClient, base, bearer(e.token)); status != 200 || body["principal"] != "operator:bearer" {
			t.Fatalf("%d %v", status, body)
		}
		if status, _ := get(http.DefaultClient, base, nil); status != 401 {
			t.Fatalf("anonymous: %d", status)
		}
	})
}

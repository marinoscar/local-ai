package cli

import (
	"encoding/base64"
	"fmt"
	"os/user"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// The clipboard side of `aictl key copy` (aictl.md section 4.8.2). The text goes to the tool on
// stdin, never in argv (a process list would show it), through execx like every program the agent
// side runs; tests replace the runner with execx/fake and read back what was run.

// CodeNoClipboard: no clipboard tool worked (LAI-CLI-011).
const CodeNoClipboard = doctor.CLI011

func init() {
	// The table of codes.go belongs to the framework; this code arrives with the command that raises it.
	codeTable[CodeNoClipboard] = codeInfo{ExitFailed, sevWarning,
		text{"No clipboard tool is available.", "Use `aictl key show` and copy the key by hand."},
		text{"No hay ninguna herramienta de portapapeles disponible.", "Use `aictl key show` y copie la clave a mano."}}
}

// Test seams: the runner, the program lookup and the user lookup.
var (
	clipRunner     execx.Runner = execx.Exec{}
	clipLookPath                = execx.Lookup
	clipLookupUser              = user.Lookup
)

const (
	clipTimeout = 5 * time.Second
	clipMaxOut  = 4096
)

// clipTool is one way to reach a clipboard: a program with a fixed argv that reads the text on stdin.
type clipTool struct {
	method string // what --json reports: the program name
	name   string // looked up in execx.FixedPath
	args   []string
}

// copyToClipboard puts secretText on the clipboard and reports the method used: wl-copy (when
// WAYLAND_DISPLAY is set), xclip then xsel (when DISPLAY is set), then OSC 52 (when stdout is a
// terminal; never with --json, where stdout is the document). Under sudo the programs run as
// $SUDO_USER with that user's runtime directory, because the clipboard belongs to the person at the
// desktop, not to root. When nothing works the error is LAI-CLI-011.
func (c *Call) copyToClipboard(secretText string) (string, error) {
	getenv := c.env.Getenv
	var tools []clipTool
	if getenv("WAYLAND_DISPLAY") != "" {
		tools = append(tools, clipTool{"wl-copy", "wl-copy", nil})
	}
	if getenv("DISPLAY") != "" {
		tools = append(tools,
			clipTool{"xclip", "xclip", []string{"-selection", "clipboard"}},
			clipTool{"xsel", "xsel", []string{"--clipboard", "--input"}})
	}
	cred, home, runtimeDir := c.sudoUser()
	for _, t := range tools {
		path, err := clipLookPath(t.name)
		if err != nil {
			continue
		}
		env := []string{"PATH=" + execx.FixedPath}
		for _, k := range []string{"WAYLAND_DISPLAY", "DISPLAY", "XAUTHORITY"} {
			if v := getenv(k); v != "" {
				env = append(env, k+"="+v)
			}
		}
		if runtimeDir == "" {
			runtimeDir = getenv("XDG_RUNTIME_DIR")
		}
		if runtimeDir != "" {
			env = append(env, "XDG_RUNTIME_DIR="+runtimeDir)
		}
		if home != "" {
			env = append(env, "HOME="+home)
		}
		res, err := clipRunner.Run(c.Ctx, execx.Cmd{
			Path: path, Args: t.args, Timeout: clipTimeout, MaxOut: clipMaxOut, Env: env,
			Stdin: strings.NewReader(secretText), Credential: cred,
		})
		if err == nil && res.ExitCode == 0 {
			return t.method, nil
		}
		if c.Ctx.Err() != nil {
			return "", c.Ctx.Err()
		}
	}
	if c.env.StdoutIsTTY && !c.Global.JSON {
		// OSC 52: the terminal puts the text on the clipboard and shows nothing.
		fmt.Fprint(c.Stdout, "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte(secretText))+"\x07")
		return "osc52", nil
	}
	return "", c.Err(CodeNoClipboard, "")
}

// sudoUser returns the identity to run the clipboard tool as when aictl runs under sudo: the user
// who typed sudo, with their home and /run/user/<uid>. Without sudo (or as that user) it is nil.
func (c *Call) sudoUser() (cred *execx.Credential, home, runtimeDir string) {
	name := c.env.Getenv("SUDO_USER")
	if name == "" || name == "root" || c.env.Geteuid == nil || c.env.Geteuid() != 0 {
		return nil, "", ""
	}
	u, err := clipLookupUser(name)
	if err != nil {
		return nil, "", ""
	}
	uid, err1 := strconv.ParseUint(u.Uid, 10, 32)
	gid, err2 := strconv.ParseUint(u.Gid, 10, 32)
	if err1 != nil || err2 != nil || uid == 0 {
		return nil, "", ""
	}
	return &execx.Credential{UID: uint32(uid), GID: uint32(gid)}, u.HomeDir, "/run/user/" + u.Uid
}

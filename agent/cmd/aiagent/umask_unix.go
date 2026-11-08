//go:build unix

package main

import (
	"os"
	"syscall"
)

// rootUmask makes a run as root create what it shares with the localai user (builds and programs under
// /opt/localai, models under /srv/localai, the catalog under /etc/localai) readable, whatever umask it
// was started with: install-node.sh runs with 077 for its own files, and a directory or file made under
// it is root-only even though the code asks for 0755 or 0644. The daemon runs under systemd's 022
// already. Every secret (keys, PIN, token, credential, state, audit) is written with an explicit 0600 or
// 0640 mode, which a umask can only narrow, never widen.
func rootUmask() { setRootUmask(os.Geteuid()) }

// setRootUmask sets the umask to 022 for euid 0 and leaves it alone for anyone else.
func setRootUmask(euid int) {
	if euid == 0 {
		syscall.Umask(0o022)
	}
}

package harden

import (
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"strconv"
	"strings"
	"time"
)

// State is what the hardening records in <state dir>/hardening.json (mode 0600). The installer summary
// (INST-07) prints Summary; the doctor checks read the rest. installer.md section 4.3 P4 step 8.
type State struct {
	Schema         int       `json:"schema"`
	Applied        bool      `json:"applied"`
	AppliedAt      time.Time `json:"applied_at"`
	RebootRequired bool      `json:"reboot_required"`
	RebootReason   string    `json:"reboot_reason,omitempty"`
	DesktopUser    string    `json:"desktop_user,omitempty"`
	Failed         bool      `json:"failed,omitempty"`
	Summary        []string  `json:"summary,omitempty"`
}

// SaveState writes the state file atomically.
func SaveState(p Paths, s State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.stateDir(), 0o700); err != nil {
		return err
	}
	return writeAtomic(p.StatePath(), append(b, '\n'), 0o600)
}

// LoadState reads the state file. A reboot that happened after AppliedAt clears RebootRequired (the pending
// flag is self-clearing, so no one has to remember to reset it). A missing file is the zero State.
func LoadState(p Paths, boot func() time.Time) (State, error) {
	var s State
	b, err := os.ReadFile(p.StatePath())
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, err
	}
	if s.RebootRequired && boot != nil {
		if bt := boot(); !bt.IsZero() && bt.After(s.AppliedAt) {
			s.RebootRequired, s.RebootReason = false, ""
		}
	}
	return s, nil
}

// bootTime is now minus /proc/uptime; the zero time when it cannot be read.
func bootTime(p Paths, now time.Time) time.Time {
	b, err := os.ReadFile(p.abs("/proc/uptime"))
	if err != nil {
		return time.Time{}
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return time.Time{}
	}
	secs, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return time.Time{}
	}
	return now.Add(-time.Duration(secs * float64(time.Second)))
}

func lookupUser(name string) (User, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return User{}, err
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return User{}, err
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return User{}, err
	}
	return User{Name: name, UID: uint32(uid), GID: uint32(gid), Home: u.HomeDir}, nil
}

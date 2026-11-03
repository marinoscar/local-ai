package units

import (
	"embed"
	"fmt"
	"regexp"
	"strings"
)

// The nightly ark sync pair (ark-sync.md section 3.7, SYNC-14). `aiagent setup units` (INST-06)
// writes them to /etc/systemd/system and runs
// `systemctl enable --now localai-sync.timer localai-drill.timer`.

// Sync is the localai-sync.timer and localai-sync.service templates.
//
//go:embed localai-sync.timer localai-sync.service
var Sync embed.FS

// The file names of the sync pair inside Sync.
const (
	SyncTimerUnit   = "localai-sync.timer"
	SyncServiceUnit = "localai-sync.service"
	// SyncExitWarn is the exit code of `aictl ark sync --scheduled` for a run that finished with
	// warnings (aictl.md section 3.4). The service lists it in SuccessExitStatus so a warning is
	// still success. Exit 9 (errors) and 3 (agent not reachable) are deliberately not listed: the
	// unit fails and shows in `systemctl --failed`.
	SyncExitWarn = 8
)

// SyncTemplates returns the embedded timer and service of the nightly sync.
func SyncTemplates() (timer, service []byte, err error) {
	if timer, err = Sync.ReadFile(SyncTimerUnit); err != nil {
		return nil, nil, err
	}
	if service, err = Sync.ReadFile(SyncServiceUnit); err != nil {
		return nil, nil, err
	}
	return timer, service, nil
}

var syncSuccessExit = regexp.MustCompile(`(?m)^\s*SuccessExitStatus\s*=\s*(.*)$`)

// LintSync checks the properties of the sync units that the rest of the system relies on and returns
// what is wrong, one problem per entry: the service asks the agent for one scheduled sync and waits,
// 8 (warnings) is a success and 9 (errors) and 3 (agent not reachable) are not, the service stops
// gracefully (SIGTERM, then the cancel request) and the timer is nightly and persistent and starts
// the service. It is a structural check; CI also runs systemd-analyze verify (scripts/verify-units.sh).
func LintSync(timer, service []byte) []string {
	var problems []string
	add := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

	svc := string(service)
	for _, want := range []string{
		"ExecStart=/usr/local/bin/aictl ark sync --scheduled --wait",
		"Type=oneshot",
		"Requires=localai-agent.service",
		"After=localai-agent.service network-online.target",
		"KillMode=mixed",
		"KillSignal=SIGTERM",
	} {
		if !strings.Contains(svc, want+"\n") {
			add("%s must contain %q", SyncServiceUnit, want)
		}
	}
	matches := syncSuccessExit.FindAllStringSubmatch(svc, -1)
	if len(matches) != 1 {
		add("%s must have exactly one SuccessExitStatus line", SyncServiceUnit)
	}
	for _, m := range matches {
		codes := strings.Fields(m[1])
		if len(codes) != 1 || codes[0] != fmt.Sprint(SyncExitWarn) {
			add("%s: SuccessExitStatus must be exactly %d (warnings), got %q", SyncServiceUnit, SyncExitWarn, strings.TrimSpace(m[1]))
		}
	}

	tm := string(timer)
	for _, want := range []string{
		"OnCalendar=*-*-* 03:15:00",
		"RandomizedDelaySec=45min",
		"Persistent=true",
		"Unit=" + SyncServiceUnit,
		"WantedBy=timers.target",
	} {
		if !strings.Contains(tm, want+"\n") {
			add("%s must contain %q", SyncTimerUnit, want)
		}
	}
	return problems
}

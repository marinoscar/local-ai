package drill

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/setup/units"
)

// The unit files of the monthly drill (drill.md section 3.3). The templates are embedded at
// internal/setup/units; `aiagent setup units` (INST-06) writes them to /etc/systemd/system and runs
// `systemctl enable --now localai-sync.timer localai-drill.timer`.
const (
	TimerUnit   = "localai-drill.timer"
	ServiceUnit = "localai-drill.service"
	// ScheduledExitWarn is the exit code of `aictl drill` for a drill that finished with warnings
	// (aictl.md section 3.4); the service lists it in SuccessExitStatus so a warning is still success.
	// Exit 9 (fail) and 3 (agent not reachable) are deliberately not listed: the unit fails and shows
	// in `systemctl --failed`.
	ScheduledExitWarn = 8
)

// Templates returns the embedded timer and service units.
func Templates() (timer, service []byte, err error) {
	timer, err = units.Drill.ReadFile(TimerUnit)
	if err != nil {
		return nil, nil, err
	}
	service, err = units.Drill.ReadFile(ServiceUnit)
	if err != nil {
		return nil, nil, err
	}
	return timer, service, nil
}

var successExit = regexp.MustCompile(`(?m)^\s*SuccessExitStatus\s*=\s*(.*)$`)

// LintUnits checks the properties of the drill units that the rest of the system relies on and
// returns what is wrong, one problem per entry: the service asks the agent for one scheduled drill and
// waits, 8 (warn) is a success and 9 (fail) and 3 (agent not reachable) are not, the timer is monthly
// and persistent and starts the service. It is a structural check; CI also runs systemd-analyze
// verify (scripts/verify-units.sh).
func LintUnits(timer, service []byte) []string {
	var problems []string
	add := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

	svc := string(service)
	if !strings.Contains(svc, "ExecStart=/usr/local/bin/aictl drill --scheduled --wait") {
		add("%s must run `aictl drill --scheduled --wait`", ServiceUnit)
	}
	if !strings.Contains(svc, "Type=oneshot") {
		add("%s must be Type=oneshot", ServiceUnit)
	}
	if !strings.Contains(svc, "Requires=localai-agent.service") {
		add("%s must require localai-agent.service", ServiceUnit)
	}
	matches := successExit.FindAllStringSubmatch(svc, -1)
	if len(matches) != 1 {
		add("%s must have exactly one SuccessExitStatus line", ServiceUnit)
	}
	for _, m := range matches {
		codes := strings.Fields(m[1])
		if len(codes) != 1 || codes[0] != fmt.Sprint(ScheduledExitWarn) {
			add("%s: SuccessExitStatus must be exactly %d (warn), got %q", ServiceUnit, ScheduledExitWarn, strings.TrimSpace(m[1]))
		}
	}

	tm := string(timer)
	for _, want := range []string{
		"OnCalendar=Sun *-*-01..07 04:30:00",
		"Persistent=true",
		"Unit=" + ServiceUnit,
		"WantedBy=timers.target",
	} {
		if !strings.Contains(tm, want) {
			add("%s must contain %q", TimerUnit, want)
		}
	}
	return problems
}

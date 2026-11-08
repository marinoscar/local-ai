package cli

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// Questions the ark commands ask instead of refusing (issue #435, ADR-0009 "nothing to memorize"):
// a person at a terminal is asked "Sync anyway over the internet?" when the node is in offline mode
// (LAI-SYN-011), and picks the drive from a numbered list when several are connected (LAI-ARK-015).
// Without a terminal, with --json or with --yes nothing is asked: the refusal and its exit code are
// exactly those of the typed command, so scripts and the cloud see no change (aictl.md section 4.10).

// errNoDriveChosen ends `ark import` without a choice (Enter alone, or the end of the input): exit 0,
// nothing started.
var errNoDriveChosen = errors.New("no drive was chosen")

// maxDriveTries is how many answers the drive question takes before it gives up.
const maxDriveTries = 3

// canAsk reports whether a question may replace a refusal: a person at a terminal, no --json, and no
// --yes (a script that passes --yes on a terminal must not hang on a question --yes cannot answer).
func (c *Call) canAsk() bool {
	return !c.Global.JSON && !c.Global.Yes && c.Term.Interactive()
}

// isOfflineRefusal reports whether err is the agent refusing a manual sync in offline mode.
func isOfflineRefusal(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusConflict && ae.Code == doctor.SYN011
}

// pickDrive shows the drives as a numbered list on stderr and reads the number of one. An answer that
// is not a number of the list is asked again, up to maxDriveTries answers; after that the result is
// several (the LAI-ARK-015 error, exit 2). Enter alone or the end of the input is errNoDriveChosen,
// Ctrl-C is exit 130.
func (c *Call) pickDrive(drives []wireDrive, several error) (string, error) {
	w := c.Stderr
	fmt.Fprintln(w, pick(c.Lang, "Several AI-ARK drives are connected. Which one do you want to copy from?",
		"Hay varias unidades AI-ARK conectadas. ¿De cuál quiere copiar?"))
	for i, d := range drives {
		label := d.name()
		if d.Mount != "" {
			label += " (" + d.Mount + ")"
		}
		fmt.Fprintf(w, "  %d. %s  %s\n", i+1, label, d.id())
	}
	for try := 1; try <= maxDriveTries; try++ {
		fmt.Fprint(w, pick(c.Lang, "Type its number and press Enter (Enter alone stops): ",
			"Escriba su número y pulse Enter (solo Enter cancela): "))
		line, err := c.Term.ReadLine(c.Ctx)
		if err != nil {
			if perr := c.endPrompt(err); perr != nil {
				return "", perr
			}
			line = "" // the end of the input: no choice
		}
		answer := strings.TrimSpace(line)
		if answer == "" {
			c.Out.Info(pick(c.Lang, "Nothing was copied.", "No se copió nada."))
			return "", errNoDriveChosen
		}
		if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(drives) {
			return drives[n-1].id(), nil
		}
		fmt.Fprintf(w, pick(c.Lang, "Type a number from 1 to %d.\n", "Escriba un número del 1 al %d.\n"), len(drives))
	}
	return "", several
}

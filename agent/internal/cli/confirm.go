package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/cli/i18n"
)

// Confirm asks a yes/no question before a destructive or surprising action (aictl.md section 3.3,
// "Confirmations"). prompt is the question with its question mark and without the choices, for
// example "Delete qwen3.6-35b-a3b-q4km from this computer (19.0 GB)?"; Confirm appends " [y/N] "
// (es: " [s/N] "). The default is always No: Enter, anything that is not a yes, and the end of the
// input (Ctrl-D) all answer false with a nil error, and the command then stops without changing
// anything.
//
//   - --yes answers true without asking.
//   - Without --yes, --json, or a stdin that is not a terminal (a pipe, a closed stdin) cannot ask:
//     the error is LAI-CLI-007, exit 2.
//   - Ctrl-C at the prompt ends the command at once with exit 130 and no message.
//
// The question is written to stderr so stdout stays clean for results. Spanish accepts s, si and sí
// as yes; both languages accept y and yes.
func (c *Call) Confirm(prompt string) (bool, error) {
	if c.Global.Yes {
		return true, nil
	}
	if err := c.requireTerminalForConfirm(false); err != nil {
		return false, err
	}
	fmt.Fprint(c.Stderr, prompt+i18n.T(c.Lang, i18n.KeyConfirmSuffix))
	line, err := c.Term.ReadLine(c.Ctx)
	if err != nil {
		return false, c.endPrompt(err)
	}
	return isYes(c.Lang, line), nil
}

// ConfirmTyped is the second, harder confirmation: the user must type expected exactly (the model
// id for `delete --allow-last-copy`). prompt is the text before the typed answer, for example "This
// is the last verified copy anywhere. Type the model id to confirm:". A different answer, an empty
// one or the end of the input is false with a nil error.
//
// --yes does NOT answer it: a typed confirmation exists so that a person decides, so with --json or
// without a terminal it is LAI-CLI-007 (exit 2) even with --yes. Ctrl-C is exit 130, as for Confirm.
func (c *Call) ConfirmTyped(prompt, expected string) (bool, error) {
	if err := c.requireTerminalForConfirm(true); err != nil {
		return false, err
	}
	fmt.Fprint(c.Stderr, prompt+" ")
	line, err := c.Term.ReadLine(c.Ctx)
	if err != nil {
		return false, c.endPrompt(err)
	}
	return expected != "" && strings.TrimSpace(line) == expected, nil
}

// requireTerminalForConfirm is the LAI-CLI-007 check shared by both helpers: a confirmation needs a
// person at a terminal, and --json means nobody is there.
func (c *Call) requireTerminalForConfirm(typed bool) error {
	if !c.Global.JSON && c.Term.Interactive() {
		return nil
	}
	e := c.Err(CodeNeedConfirm, "")
	if typed {
		e.Fix = pick(c.Lang,
			"Run it in a terminal. `--yes` does not answer this question.",
			"Ejecútelo en una terminal. `--yes` no responde a esta pregunta.")
	}
	return e
}

// endPrompt turns the end of a prompt read into the command's result: Ctrl-C is exit 130 with no
// message (nothing had started, so "still running in the background" would be false), a deadline
// is the timeout error, and the end of the input is the default answer, No.
func (c *Call) endPrompt(err error) error {
	fmt.Fprintln(c.Stderr) // the terminal did not echo the Enter key (or Ctrl-C)
	switch {
	case errors.Is(err, context.Canceled):
		return ExitWith(ExitInterrupted)
	case errors.Is(err, io.EOF):
		return nil
	}
	return err
}

// isYes reports whether line is an affirmative answer in lang.
func isYes(lang Lang, line string) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	case "s", "si", "sí":
		return lang == LangES
	}
	return false
}

package cli

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// `aictl ui-code` (aictl.md section 4.20): a one-use login code for a plain browser, for the person
// who has the terminal but not the desktop app. POST /ui-code answers for root and for a member of
// localai-operators over the socket.

// uiCode is POST /ui-code (agent.md section 3.8.2).
type uiCode struct {
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"`
}

// uiCodeNow is the clock of "valid for 5 minutes" (a test seam).
var uiCodeNow = time.Now

func init() {
	Register(&Command{
		Name:    "ui-code",
		Summary: "Print a one-time code to sign in to the local web page.",
		Description: "Open the address it prints in a browser and type the code. The code works once and expires after a few\n" +
			"minutes. It needs root or membership of the localai-operators group.",
		Timeout: 10 * time.Second,
		Run:     uiCodeRun,
	})
}

func uiCodeRun(c *Call) error {
	if err := c.noArgs(); err != nil {
		return err
	}
	var res uiCode
	raw, _, err := c.call(http.MethodPost, "/ui-code", map[string]any{}, &res)
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return c.emitRaw(raw)
	}
	if res.Code == "" {
		return c.Err(CodeBadResponse, "").WithDetails("reason", "no code in the answer")
	}
	minutes := 5
	if t, perr := time.Parse(time.RFC3339, res.ExpiresAt); perr == nil {
		if m := int((t.Sub(uiCodeNow()) + 30*time.Second) / time.Minute); m >= 1 {
			minutes = m
		}
	}
	shown := res.Code
	if len(shown) == 6 {
		shown = shown[:3] + " " + shown[3:] // 482 913: easier to read and to type
	}
	unit := pick(c.Lang, "minutes", "minutos")
	if minutes == 1 {
		unit = pick(c.Lang, "minute", "minuto")
	}
	c.Out.Result(pick(c.Lang,
		fmt.Sprintf("Login code  %s   (valid for %d %s, one use)", shown, minutes, unit),
		fmt.Sprintf("Código de acceso  %s   (válido por %d %s, un solo uso)", shown, minutes, unit)))
	portal := strings.TrimRight(c.Global.URL, "/")
	c.Out.Result(pick(c.Lang,
		"Open "+portal+" in a browser and type this code.",
		"Abra "+portal+" en un navegador y escriba este código."))
	return nil
}

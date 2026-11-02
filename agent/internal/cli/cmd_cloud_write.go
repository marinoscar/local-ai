package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// The `aictl cloud` commands that change something (aictl.md section 4.18): enroll, unenroll and
// resend-key. `cloud status` is AICTL-04. The node credential (nod_...) is a secret: it is read from
// a prompt without echo or from one line of stdin, never from an argument or the environment, and it
// goes only into the body of POST /cloud/enroll.

const defaultCloudURL = "https://ai.marin.cr"

// cloudEnrolled is POST /cloud/enroll (agent.md section 3.8.26).
type cloudEnrolled struct {
	State        string `json:"state"`
	ServerNodeID string `json:"server_node_id"`
	Reattached   bool   `json:"reattached"`
	LastError    string `json:"last_error"`
}

func init() {
	attachAll(
		at(&Command{
			Name: "enroll", Summary: "Connect this node to the cloud with its node credential.", Marks: MarkAdmin,
			Usage: "[--url <url>] [--credential-stdin]", Timeout: 60 * time.Second,
			Description: `Asks for the node credential without showing it (or reads one line from stdin with
--credential-stdin; with --pin-stdin too, the credential line comes first). The credential is never
accepted as an argument. --url defaults to ` + defaultCloudURL + `.
If the cloud cannot be reached the credential is kept and the node connects by itself later.`,
			Flags: func(fs *flag.FlagSet) {
				fs.String("url", defaultCloudURL, "the address of the cloud")
				fs.Bool("credential-stdin", false, "read the node credential from one line of stdin")
			},
			Run: cloudEnroll,
		}, "cloud"),
		at(&Command{
			Name: "unenroll", Summary: "Disconnect this node from the cloud.", Marks: MarkAdmin,
			Usage: "[--yes]", Timeout: 30 * time.Second, Run: cloudUnenroll,
		}, "cloud"),
		at(&Command{
			Name: "resend-key", Summary: "Send the API key to the cloud again.", Marks: MarkAdmin,
			Timeout:     30 * time.Second,
			Description: "Use it when the cloud lost the key (for example after its encryption key changed). Needs online mode.",
			Run:         cloudResendKey,
		}, "cloud"),
	)
}

// readCredential reads the node credential: one line from stdin with --credential-stdin, else a
// prompt without echo on the terminal. It checks the shape (nod_ prefix) without ever printing it.
func (c *Call) readCredential() (string, error) {
	var line string
	var err error
	switch {
	case c.Bool("credential-stdin"):
		line, err = c.Term.ReadLine(c.Ctx)
	case c.Global.JSON || !c.Term.Interactive():
		return "", c.usageErr(
			pick(c.Lang, "The node credential needs a terminal to be typed in.", "La credencial del nodo necesita una terminal para escribirla."),
			pick(c.Lang, "Run it in a terminal, or pipe the credential in and add `--credential-stdin`.",
				"Ejecútelo en una terminal, o envíe la credencial por stdin y agregue `--credential-stdin`."))
	default:
		fmt.Fprint(c.Stderr, pick(c.Lang, "Node credential (nod_…): ", "Credencial del nodo (nod_…): "))
		line, err = c.Term.ReadSecret(c.Ctx)
		fmt.Fprintln(c.Stderr) // echo was off: end the prompt line ourselves
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "", ExitWith(ExitInterrupted)
	case err != nil && !errors.Is(err, io.EOF):
		return "", err
	}
	cred := strings.TrimSpace(line)
	if cred == "" {
		return "", c.usageErr(pick(c.Lang, "No credential was entered.", "No se escribió ninguna credencial."), "")
	}
	if !strings.HasPrefix(cred, "nod_") {
		return "", c.usageErr(
			pick(c.Lang, "That is not a node credential: it starts with nod_.", "Eso no es una credencial de nodo: empieza con nod_."),
			pick(c.Lang, "Create one on the Nodes page of the cloud app and paste it again.",
				"Cree una en la página Nodos de la aplicación en la nube y péguela otra vez."))
	}
	return cred, nil
}

// cloudURLArg checks --url: an http or https address with a host, no user name and no password.
func (c *Call) cloudURLArg() (string, error) {
	raw := strings.TrimSpace(c.String("url"))
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return "", c.usageErr(
			pick(c.Lang, fmt.Sprintf("--url %q is not a web address.", raw), fmt.Sprintf("--url %q no es una dirección web.", raw)),
			pick(c.Lang, "Use an address like "+defaultCloudURL+".", "Use una dirección como "+defaultCloudURL+"."))
	}
	return strings.TrimRight(raw, "/"), nil
}

func cloudEnroll(c *Call) error {
	if len(c.Args) > 0 {
		// Someone typed the credential after the command. It is never taken from an argument, and
		// the error must not repeat it: the shell history already holds it, the screen need not.
		return c.usageErr(
			pick(c.Lang, "The node credential is never given as an argument.", "La credencial del nodo nunca se da como argumento."),
			pick(c.Lang, "Run `aictl cloud enroll` and type it when asked, or pipe it in with `--credential-stdin`. If it was typed in a shell, make a new one on the Nodes page.",
				"Ejecute `aictl cloud enroll` y escríbala cuando se pida, o envíela por stdin con `--credential-stdin`. Si la escribió en una terminal, cree una nueva en la página Nodos."))
	}
	cloudURL, err := c.cloudURLArg()
	if err != nil {
		return err
	}
	cred, err := c.readCredential()
	if err != nil {
		return err
	}
	var res cloudEnrolled
	var raw json.RawMessage
	err = c.Admin(func() (e error) {
		raw, _, e = c.call(http.MethodPost, "/cloud/enroll", map[string]string{"url": cloudURL, "credential": cred}, &res)
		return e
	})
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return c.emitRaw(raw)
	}
	switch res.State {
	case "backoff":
		// Saved, but the cloud did not answer: not a failure, the link retries by itself.
		c.Out.Problem(out.Problem{Severity: out.SevWarning, Code: doctor.CLD053, Message: pick(c.Lang,
			"Saved. The cloud is not reachable right now; the node will connect by itself.",
			"Guardado. La nube no responde en este momento; el nodo se conectará solo."), Details: map[string]any{"last_error": res.LastError}})
	case "stopped":
		c.Out.Info(pick(c.Lang,
			"Saved. The node connects to the cloud when it is in online mode.",
			"Guardado. El nodo se conectará a la nube cuando esté en modo en línea."))
	default:
		name := c.cloudNodeName()
		msg := pick(c.Lang, "Connected to "+cloudURL, "Conectado a "+cloudURL)
		if name != "" {
			msg += pick(c.Lang, " as "+name, " como "+name)
		}
		msg += pick(c.Lang, ". The node shows up on the Nodes page.", ". El nodo aparece en la página Nodos.")
		if res.Reattached {
			msg += pick(c.Lang, " Its earlier record was reused.", " Se reutilizó su registro anterior.")
		}
		c.Out.OK(msg)
	}
	return nil
}

// cloudNodeName is the name the cloud knows this node by (GET /cloud), or "" when it cannot be read:
// the line it ends is worded without a name rather than failing a command that already succeeded.
func (c *Call) cloudNodeName() string {
	ctx, cancel := context.WithTimeout(c.Ctx, 5*time.Second)
	defer cancel()
	cl, err := c.Client()
	if err != nil {
		return ""
	}
	var info struct {
		Name string `json:"name"`
	}
	if _, _, err := c.callWith(cl, ctx, http.MethodGet, "/cloud", nil, &info); err != nil {
		return ""
	}
	return info.Name
}

func cloudUnenroll(c *Call) error {
	if err := c.noArgs(); err != nil {
		return err
	}
	ok, err := c.Confirm(pick(c.Lang,
		"Disconnect this node from the cloud? The cloud will not see it until you enroll it again.",
		"¿Desconectar este nodo de la nube? La nube no lo verá hasta que lo vuelva a registrar."))
	if err != nil || !ok {
		return err
	}
	var raw json.RawMessage
	err = c.Admin(func() (e error) {
		raw, _, e = c.call(http.MethodDelete, "/cloud", nil, nil)
		return e
	})
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return c.emitRaw(raw)
	}
	c.Out.OK(pick(c.Lang, "This node is no longer connected to the cloud.", "Este nodo ya no está conectado a la nube."))
	return nil
}

func cloudResendKey(c *Call) error {
	if err := c.noArgs(); err != nil {
		return err
	}
	var res struct {
		KeySynced   bool   `json:"key_synced"`
		Fingerprint string `json:"fingerprint"`
	}
	var raw json.RawMessage
	err := c.Admin(func() (e error) {
		raw, _, e = c.call(http.MethodPost, "/cloud/resend-key", map[string]any{}, &res)
		return e
	})
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return c.emitRaw(raw)
	}
	if !res.KeySynced {
		c.Out.Warn(pick(c.Lang, "The cloud did not take the key. Try again in a moment.", "La nube no aceptó la clave. Inténtelo de nuevo en un momento."))
		return ExitWith(ExitFailed)
	}
	c.Out.OK(pick(c.Lang,
		"The API key (fingerprint "+res.Fingerprint+") was sent to the cloud again.",
		"La clave de la API (huella "+res.Fingerprint+") se envió de nuevo a la nube."))
	return nil
}

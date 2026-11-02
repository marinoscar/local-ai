package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
)

// The `aictl key` group (aictl.md section 4.8): show, copy, rotate, snippets. The key is a secret:
// it is printed only by `key show` (after the warning on stderr), `key snippets --with-key` and the
// --json bodies of those; it never goes in argv, a log line or an error.

// keyInfo is GET /key (agent.md section 3.8.13).
type keyInfo struct {
	Key         string `json:"key"`
	Fingerprint string `json:"fingerprint"`
	BaseURLs    struct {
		Local   string  `json:"local"`
		Tailnet *string `json:"tailnet"`
		Public  *string `json:"public"`
	} `json:"base_urls"`
	Models   []string `json:"models"`
	Snippets struct {
		Curl         string          `json:"curl"`
		Env          string          `json:"env"`
		OpenCode     json.RawMessage `json:"opencode"`
		OpenAIPython string          `json:"openai_python"`
	} `json:"snippets"`
}

// keyRotated is POST /key/rotate (agent.md section 3.8.14).
type keyRotated struct {
	Fingerprint     string   `json:"fingerprint"`
	RotatedAt       string   `json:"rotated_at"`
	CloudSynced     bool     `json:"cloud_synced"`
	RestoredModels  []string `json:"restored_models"`
	ClientsToUpdate []string `json:"clients_to_update"`
	OpID            string   `json:"op_id"`
}

func init() {
	Register(&Command{
		Name:    "key",
		Summary: "Show, copy or change the API key of this node.",
		Sub: []*Command{
			{
				Name: "show", Summary: "Print the API key and the addresses to use it.", Timeout: 10 * time.Second,
				Description: "The only command that prints the whole key. The key is secret: do not paste it in chats or emails.\n" +
					"With --json it prints the answer of the agent (key, fingerprint, addresses, models, snippets).",
				Run: keyShow,
			},
			{
				Name: "copy", Summary: "Put the API key on the clipboard without printing it.", Timeout: 10 * time.Second,
				Description: "Tries wl-copy (Wayland), xclip or xsel (X11), then the terminal's own clipboard (OSC 52).\n" +
					"With none of them it fails with LAI-CLI-011: use `aictl key show` instead.",
				Run: keyCopy,
			},
			{
				Name: "rotate", Summary: "Create a new API key (the old one stops working).", Marks: MarkAdmin,
				Timeout: 15 * time.Minute, Usage: "[--yes]",
				Description: "Asks first. The model service is restarted with the new key; the cloud and the gateway are updated by themselves.",
				Run:         keyRotate,
			},
			{
				Name: "snippets", Summary: "Print ready-to-paste examples that use the key.", Timeout: 10 * time.Second,
				Usage: "[--for curl|env|opencode|python] [--url local|tailnet|public] [--with-key]",
				Description: "The key appears as $LLAMA_API_KEY unless you ask for --with-key (then it is printed in full).\n" +
					"With --json it prints {\"snippets\": {...}, \"base_url\": \"...\"}.",
				Flags: func(fs *flag.FlagSet) {
					fs.String("for", "", "only one kind of example: curl, env, opencode or python (default: all)")
					fs.String("url", "local", "which address to use: local, tailnet or public")
					fs.Bool("with-key", false, "print the real key instead of $LLAMA_API_KEY")
				},
				Run: keySnippets,
			},
		},
	})
}

func keyShow(c *Call) error {
	if err := c.noArgs(); err != nil {
		return err
	}
	var k keyInfo
	raw, _, err := c.call(http.MethodGet, "/key", nil, &k)
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return c.emitRaw(raw)
	}
	if k.Key == "" {
		return c.Err(CodeBadResponse, "").WithDetails("reason", "no key in the answer")
	}
	c.Out.PrintSecretWarning()
	none := pick(c.Lang, "not available", "no disponible")
	c.Out.KV([]out.Pair{
		{Label: pick(c.Lang, "Key", "Clave"), Value: k.Key},
		{Label: pick(c.Lang, "Fingerprint", "Huella"), Value: k.Fingerprint},
		{Label: pick(c.Lang, "Local URL", "URL local"), Value: orText(k.BaseURLs.Local, none)},
		{Label: pick(c.Lang, "Tailnet URL", "URL tailnet"), Value: orText(deref(k.BaseURLs.Tailnet), none)},
		{Label: pick(c.Lang, "Public URL", "URL pública"), Value: orText(deref(k.BaseURLs.Public), none)},
		{Label: pick(c.Lang, "Models", "Modelos"), Value: orText(strings.Join(k.Models, ", "), pick(c.Lang, "none", "ninguno"))},
	})
	return nil
}

func keyCopy(c *Call) error {
	if err := c.noArgs(); err != nil {
		return err
	}
	var k keyInfo
	if _, _, err := c.call(http.MethodGet, "/key", nil, &k); err != nil {
		return err
	}
	if k.Key == "" {
		return c.Err(CodeBadResponse, "").WithDetails("reason", "no key in the answer")
	}
	method, err := c.copyToClipboard(k.Key)
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return c.Out.JSON().Document(map[string]any{"copied": true, "method": method})
	}
	c.Out.OK(pick(c.Lang,
		"The API key is on the clipboard. Paste it where it is needed, then copy something else.",
		"La clave de la API está en el portapapeles. Péguela donde la necesite y luego copie otra cosa."))
	return nil
}

func keyRotate(c *Call) error {
	if err := c.noArgs(); err != nil {
		return err
	}
	ok, err := c.Confirm(pick(c.Lang,
		"Programs using the old key will stop working until you update them. Continue?",
		"Los programas que usan la clave anterior dejarán de funcionar hasta que los actualice. ¿Continuar?"))
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	var res keyRotated
	var raw json.RawMessage
	err = c.Admin(func() error {
		var e error
		raw, _, e = c.call(http.MethodPost, "/key/rotate", map[string]any{"restart": true}, &res)
		return e
	})
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.Details["key_rotated"] == true {
			// The model did not restart, but the new key is in place and the old one is gone.
			c.Out.Warn(pick(c.Lang,
				"The new key is in place, but the model service did not start with it. Copy the key with `aictl key copy` and start the model with `aictl restart llama`.",
				"La clave nueva ya está en uso, pero el servicio de modelos no arrancó con ella. Copie la clave con `aictl key copy` y arranque el modelo con `aictl restart llama`."))
		}
		return err
	}
	if c.Global.JSON {
		return c.emitRaw(raw)
	}
	head := pick(c.Lang, "New key created (fingerprint "+res.Fingerprint+").", "Clave nueva creada (huella "+res.Fingerprint+").")
	switch n := len(res.RestoredModels); {
	case n == 1:
		head += pick(c.Lang, " The model was started again.", " El modelo se inició de nuevo.")
	case n > 1:
		head += pick(c.Lang, " The models were started again.", " Los modelos se iniciaron de nuevo.")
	}
	c.Out.OK(head)
	if len(res.ClientsToUpdate) > 0 {
		c.Out.Result("  " + pick(c.Lang,
			"Update these programs with the new key (`aictl key copy`):",
			"Actualice estos programas con la clave nueva (`aictl key copy`):"))
		for _, who := range res.ClientsToUpdate {
			c.Out.Result("   • " + who)
		}
	}
	if res.CloudSynced {
		c.Out.Result("  " + pick(c.Lang,
			"The cloud and the gateway were updated automatically.",
			"La nube y la pasarela se actualizaron solas."))
	} else {
		c.Out.Warn(pick(c.Lang,
			"The cloud has not received the new key yet. It gets it when the link starts; run `aictl cloud resend-key` to send it now.",
			"La nube aún no recibió la clave nueva. La recibirá cuando se inicie el enlace; ejecute `aictl cloud resend-key` para enviarla ahora."))
	}
	return nil
}

// keySnippetKinds maps the --for words to the keys of the agent's snippets object.
var keySnippetKinds = []struct{ flag, field string }{
	{"curl", "curl"}, {"env", "env"}, {"opencode", "opencode"}, {"python", "openai_python"},
}

func keySnippets(c *Call) error {
	if err := c.noArgs(); err != nil {
		return err
	}
	only := c.String("for")
	if only != "" {
		found := false
		for _, k := range keySnippetKinds {
			found = found || k.flag == only
		}
		if !found {
			return c.usageErr(pick(c.Lang, "--for must be curl, env, opencode or python.", "--for debe ser curl, env, opencode o python."), "")
		}
	}
	which := c.String("url")
	withKey := c.Bool("with-key")

	var k keyInfo
	if _, _, err := c.call(http.MethodGet, "/key", nil, &k); err != nil {
		return err
	}
	var base string
	switch which {
	case "local", "":
		base = k.BaseURLs.Local
	case "tailnet":
		base = deref(k.BaseURLs.Tailnet)
	case "public":
		base = deref(k.BaseURLs.Public)
	default:
		return c.usageErr(pick(c.Lang, "--url must be local, tailnet or public.", "--url debe ser local, tailnet o public."), "")
	}
	if base == "" {
		return c.usageErr(
			pick(c.Lang, "This node has no "+which+" address right now.", "Este nodo no tiene una dirección "+which+" en este momento."),
			pick(c.Lang, "Use `--url local`, or check `aictl status`.", "Use `--url local` o revise `aictl status`."))
	}

	// What stands for the key in the text: the variable, or the key itself.
	placeholder := "$LLAMA_API_KEY"
	if withKey {
		placeholder = k.Key
	}
	swap := func(s string) string {
		s = strings.ReplaceAll(s, k.BaseURLs.Local, base)
		s = strings.ReplaceAll(s, "$KEY", placeholder)
		return strings.ReplaceAll(s, "<key>", placeholder)
	}
	type snippet struct {
		kind, field string
		text        string // human block
		value       any    // --json value
	}
	var list []snippet
	for _, kind := range keySnippetKinds {
		if only != "" && kind.flag != only {
			continue
		}
		s := snippet{kind: kind.flag, field: kind.field}
		switch kind.flag {
		case "curl":
			plain := swap(k.Snippets.Curl)
			s.value = plain
			s.text = wrapCurl(plain)
			if !withKey {
				s.text = "export LLAMA_API_KEY=$(aictl key show --json | jq -r .key)\n" + s.text
			}
		case "env":
			s.value = swap(k.Snippets.Env)
			s.text = swap(k.Snippets.Env)
		case "opencode":
			text := swap(string(k.Snippets.OpenCode))
			var v any
			if json.Unmarshal([]byte(text), &v) == nil {
				s.value = v
				var buf bytes.Buffer
				if json.Indent(&buf, []byte(text), "", "  ") == nil {
					text = buf.String()
				}
			}
			s.text = text
		case "python":
			py := k.Snippets.OpenAIPython
			if withKey {
				py = strings.ReplaceAll(py, "api_key=KEY", `api_key="`+k.Key+`"`)
			} else {
				py = "import os\n" + strings.ReplaceAll(py, "api_key=KEY", `api_key=os.environ["LLAMA_API_KEY"]`)
			}
			py = strings.ReplaceAll(py, k.BaseURLs.Local, base)
			s.value, s.text = py, py
		}
		if s.text == "" || s.text == "null" {
			continue // the agent had no such snippet
		}
		list = append(list, s)
	}

	if c.Global.JSON {
		m := map[string]any{}
		for _, s := range list {
			m[s.field] = s.value
		}
		return c.Out.JSON().Document(map[string]any{"snippets": m, "base_url": base})
	}
	if withKey {
		c.Out.PrintSecretWarning()
	}
	for i, s := range list {
		if i > 0 {
			c.Out.Result("")
		}
		if only == "" {
			c.Out.Result("# " + s.kind)
		}
		c.Out.Result(s.text)
	}
	return nil
}

// wrapCurl puts the headers and the body of the agent's one-line curl on their own lines.
func wrapCurl(s string) string {
	s = strings.Replace(s, " -H 'Content-Type", " \\\n  -H 'Content-Type", 1)
	return strings.Replace(s, " -d '", " \\\n  -d '", 1)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func orText(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

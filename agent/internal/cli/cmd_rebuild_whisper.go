package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/cli/local"
	"github.com/marinoscar/local-ai/agent/internal/cli/out"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/router/build"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"gopkg.in/yaml.v3"
)

// `aictl rebuild-whisper` (aictl.md section 4.23a, companion-services.md section 4.2.1, SVC-05). It
// runs in this process as root: the build manager of internal/router/build with WhisperRecipe turns
// the pinned whisper.cpp commit into /opt/localai/whisper.cpp/builds/<commit9>-<backend>, tests it,
// switches `current` and restarts the speech service through the agent (or systemctl when the agent
// is down). This file is the flags, the node.yaml and kit wiring, the messages and the exit codes;
// the work is the engine's. `--prebuilt` and the export of a whisper build are KIT-05.

const (
	// rebuildWhisperTimeout is the default deadline: a CPU build takes minutes, a CUDA build on a
	// laptop most of an hour (aictl.md section 4.23a says 1 h).
	rebuildWhisperTimeout = time.Hour
	// whisperProbeTimeout bounds one liveness probe of whisper-server.
	whisperProbeTimeout = 3 * time.Second
)

var whisperBackends = []string{"auto", "cuda", "vulkan", "cpu"}

var whisperCommitRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

func init() {
	Register(&Command{
		Name:    "rebuild-whisper",
		Summary: "Rebuild the speech-to-text program (whisper.cpp) from the pinned source.",
		Usage:   "[--commit <sha>] [--backend auto|cuda|vulkan|cpu] [--source <dir> | --from-kit] [--activate | --no-activate] [--dry-run] [--yes]",
		Description: `Builds whisper.cpp from the commit pinned in node.yaml (whisper.commit), tests the new build (it
must be self-contained, answer --help and, when a speech model is installed, transcribe a short
recording), and only then switches /opt/localai/whisper.cpp/current to it and restarts the speech
service. If the test or the restart fails, the previous build stays in use. The last two builds are kept.
  --commit <sha>     Build this commit (7 to 40 hex characters) instead of whisper.commit.
  --backend <name>   cuda, vulkan or cpu; auto (the default) is the backend of the AI model service
                     (llama.backend). --backend cpu keeps all the graphics memory for the AI.
  --source <dir>     Build from this folder instead of the kit.
  --from-kit         Use only the rebuild kit as the source: no internet is used.
  --activate         Switch to the new build when it passes its test (the default).
  --no-activate      Build and test it, but do not switch to it.
  --dry-run          Only check that everything needed is there; change nothing. Exit 0 only if every
                     check passes.
  --yes              Do not ask for confirmation.
Without --source the source is the kit's tarball of the commit, so a build with and without internet
is the same. Run it with sudo.
Specification: docs/specs/aictl.md section 4.23a, docs/specs/companion-services.md section 4.2.1`,
		Marks:   MarkLocal | MarkRoot,
		Timeout: rebuildWhisperTimeout,
		Flags: func(fs *flag.FlagSet) {
			fs.String("commit", "", "build this commit (7 to 40 hex characters)")
			fs.String("backend", "auto", "auto, cuda, vulkan or cpu")
			fs.String("source", "", "build from this folder")
			fs.Bool("from-kit", false, "use only the rebuild kit as the source")
			fs.Bool("activate", false, "switch to the new build when it passes its test (the default)")
			fs.Bool("no-activate", false, "build and test it without switching to it")
			fs.Bool("dry-run", false, "check what is needed and change nothing")
			declareYes(fs)
		},
		Run: rebuildWhisper,
	})
}

// ---- the seams to the machine -------------------------------------------------------------------

// whisperEngine is the part of *build.Manager the command uses; tests replace it.
type whisperEngine interface {
	Resolve(ctx context.Context, req build.Request) (build.Inputs, error)
	Build(ctx context.Context, req build.Request) (build.Result, error)
}

// whisperNewEngine makes the engine from the options the command assembled.
var whisperNewEngine = func(opt build.Options) whisperEngine { return build.New(opt) }

// whisperProbeFn asks whisper-server whether it is alive: GET <url> answers 200 (companion-services.md
// section 4.6). Tests replace it.
var whisperProbeFn = func(ctx context.Context, rawURL string) error {
	ctx, cancel := context.WithTimeout(ctx, whisperProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the speech server answered HTTP %d", resp.StatusCode)
	}
	return nil
}

// whisperCompileUser is Options.CompileUser; tests set build.CompileAsCaller.
var whisperCompileUser = ""

// ---- node.yaml ---------------------------------------------------------------------------------

// whisperSettings are the values of node.yaml the rebuild reads.
type whisperSettings struct {
	// Commit and Backend are whisper.commit and whisper.backend as written ("" when absent).
	Commit, Backend string
	// Enabled is whisper.enabled (true when absent).
	Enabled bool
	// LlamaBackend and CudaArch are llama.backend and llama.cuda_arch.
	LlamaBackend, CudaArch string
	// Port is ports.whisper.
	Port int
}

// readWhisperSettings reads node.yaml. A missing or unreadable file is not an error: a node being
// rebuilt from the kit may have none, and --commit and --backend then have to say everything.
func readWhisperSettings(env local.Env) whisperSettings {
	s := whisperSettings{Enabled: true, Port: config.DefaultWhisperPort}
	data, err := os.ReadFile(env.Paths.NodeYAML())
	if err != nil || len(data) > 1<<20 {
		return s
	}
	cfg, _ := config.Parse(data, nil)
	s.LlamaBackend = cfg.Llama.Backend
	if cfg.Llama.CudaArch != nil {
		s.CudaArch = *cfg.Llama.CudaArch
	}
	if cfg.Ports.Whisper > 0 {
		s.Port = cfg.Ports.Whisper
	}
	if raw, ok := cfg.Blocks["whisper"]; ok {
		var b struct {
			Enabled *bool  `yaml:"enabled"`
			Commit  string `yaml:"commit"`
			Backend string `yaml:"backend"`
		}
		if raw.Decode(&b) == nil {
			s.Commit, s.Backend = strings.TrimSpace(b.Commit), strings.TrimSpace(b.Backend)
			if b.Enabled != nil {
				s.Enabled = *b.Enabled
			}
		}
	}
	return s
}

// recordWhisperCommit writes whisper.commit into node.yaml (the other keys and the comments stay as
// they are).
func recordWhisperCommit(env local.Env, commit string) error {
	store := config.Open(config.Options{Path: env.Paths.NodeYAML()})
	return store.Update(func(root *yaml.Node) error {
		return config.SetKey(root, commit, "whisper", "commit")
	})
}

// ---- the agent ---------------------------------------------------------------------------------

// whisperServices is build.ServiceRestarter over the agent's HTTP API: POST /services/whisper/restart.
type whisperServices struct{ c *Call }

// Up reports whether the agent answers.
func (s whisperServices) Up(ctx context.Context) bool {
	cl, err := s.c.Client()
	if err != nil {
		return false
	}
	_, err = cl.Do(ctx, http.MethodGet, "/health", nil, nil)
	return err == nil
}

// RestartService restarts a service through the agent. A busy agent is a *build.BusyError, one that
// stopped answering build.ErrAgentUnavailable.
func (s whisperServices) RestartService(ctx context.Context, name string) error {
	cl, err := s.c.Client()
	if err != nil {
		return fmt.Errorf("%w: %v", build.ErrAgentUnavailable, err)
	}
	_, err = cl.Do(ctx, http.MethodPost, "/services/"+url.PathEscape(name)+"/restart", map[string]any{}, nil)
	var ae *APIError
	var ce *CLIError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ae) && ae.Status == http.StatusConflict && ae.Code == doctor.AGT022:
		return &build.BusyError{OpID: ae.OpID}
	case errors.As(err, &ce) && ce.Code == CodeUnreachable:
		return fmt.Errorf("%w: %v", build.ErrAgentUnavailable, err)
	}
	return err
}

// ---- the speech model of the smoke test --------------------------------------------------------

// whisperSmokeModel returns the smallest verified whisper model on this computer, from the content
// roots (models/<id>/MANIFEST.json of engine whisper, state verified): the model the speech service
// itself would use is SVC-06's choice, and a smaller one answers the smoke test faster. ok is false
// when there is none.
func whisperSmokeModel(env local.Env) (string, bool) {
	type cand struct {
		size int64
		path string
	}
	var found []cand
	for _, r := range local.FindRoots(context.Background(), env) {
		dirs, err := os.ReadDir(filepath.Join(r.Path, "models"))
		if err != nil {
			continue
		}
		for _, d := range dirs {
			if !d.IsDir() {
				continue
			}
			dir := filepath.Join(r.Path, "models", d.Name())
			m, err := manifest.Load(config.OSFS{}, dir)
			if err != nil || m.Engine != "whisper" {
				continue
			}
			for _, f := range m.Files {
				if f.Kind == "whisper" && manifest.ValidFilePath(f.Path) {
					found = append(found, cand{f.SizeBytes, filepath.Join(dir, filepath.FromSlash(f.Path))})
				}
			}
		}
	}
	if len(found) == 0 {
		return "", false
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].size != found[j].size {
			return found[i].size < found[j].size
		}
		return found[i].path < found[j].path
	})
	return found[0].path, true
}

// localAudit adapts the audit log of the local commands to build.AuditWriter; a node with no state
// folder yet (a rebuild from scratch) has no audit log to write to.
type localAudit struct{ env local.Env }

func (a localAudit) Write(ev audit.Event) error {
	if fi, err := os.Stat(a.env.Paths.StateDir); err != nil || !fi.IsDir() {
		return nil
	}
	return a.env.Audit(ev.Action, ev.Result, ev.Code, ev.Target)
}

// ---- the command -------------------------------------------------------------------------------

func rebuildWhisper(c *Call) error {
	if err := noExtraArgs(c); err != nil {
		return err
	}
	applyYes(c)
	req, err := whisperRequest(c)
	if err != nil {
		return err
	}
	if err := RequireRoot(c); err != nil {
		return err
	}
	env := localEnv()
	set := readWhisperSettings(env)

	var kitPaths []string
	roots, rerr := kitRoots(c, env, "")
	if rerr != nil && req.FromKit {
		return rerr // --from-kit with no kit anywhere: LAI-KIT-040, exit 5
	}
	for _, r := range roots {
		kitPaths = append(kitPaths, r.Path)
	}
	release, err := kitLock(c, env)
	if err != nil {
		return err
	}
	defer release()

	opt := build.Options{
		Recipe:      build.WhisperRecipe,
		BuiltBy:     "aictl " + c.env.Version,
		KitRoots:    func() []string { return kitPaths },
		Sources:     build.ChainWithKit(build.SourceStemWhisper, kitPaths, build.DefaultWhisperCloneURL),
		CompileUser: whisperCompileUser,
		Audit:       localAudit{env},
		Defaults: func() build.Defaults {
			l := config.LlamaBlock{Backend: set.LlamaBackend}
			if set.CudaArch != "" {
				arch := set.CudaArch
				l.CudaArch = &arch
			}
			return build.WhisperDefaults(set.Commit, set.Backend, l)
		},
		Services: whisperServices{c},
		ServiceCheck: func(ctx context.Context) error {
			if !set.Enabled {
				return fmt.Errorf("speech is switched off in node.yaml: %w", build.ErrServiceNotConfigured)
			}
			if _, err := os.Stat(filepath.Join(env.Paths.EtcDir, "whisper.env")); err != nil {
				return fmt.Errorf("the speech service has no whisper.env yet: %w", build.ErrServiceNotConfigured)
			}
			return whisperProbeFn(ctx, "http://127.0.0.1:"+strconv.Itoa(set.Port)+"/")
		},
		SmokeModel:   func() (string, bool) { return whisperSmokeModel(env) },
		RecordCommit: func(commit string) error { return recordWhisperCommit(env, commit) },
	}
	eng := whisperNewEngine(opt)

	req.Progress = func(p build.Progress) {
		if !c.Global.JSON {
			c.Out.Progress(p.Message)
		}
	}
	if req.DryRun {
		return whisperDryRun(c, eng, req)
	}

	in, err := eng.Resolve(c.Ctx, req)
	if err != nil {
		return whisperErr(c, err)
	}
	if !c.Global.JSON {
		c.Out.Info(whisperPlanLine(c.Lang, in, req))
	}
	ok, cerr := c.Confirm(whisperConfirmPrompt(c.Lang, in, req))
	if cerr != nil {
		return cerr
	}
	if !ok {
		c.Out.Info(pick(c.Lang, "Nothing was changed.", "No se cambió nada."))
		return ExitWith(ExitRefused)
	}
	res, err := eng.Build(c.Ctx, req)
	if err != nil {
		return whisperErr(c, err)
	}
	return whisperDone(c, res, req)
}

// whisperRequest reads and checks the flags (before anything is touched).
func whisperRequest(c *Call) (build.Request, error) {
	var req build.Request
	req.Commit = strings.ToLower(strings.TrimSpace(c.String("commit")))
	if req.Commit != "" && !whisperCommitRe.MatchString(req.Commit) {
		return req, c.usageErr(
			pick(c.Lang, "The commit "+quote(req.Commit)+" must be 7 to 40 lowercase hex characters.",
				"El commit "+quote(req.Commit)+" debe tener de 7 a 40 caracteres hexadecimales en minúscula."),
			pick(c.Lang, "Copy it from `git log`, or leave --commit out to use whisper.commit of node.yaml.",
				"Cópielo de `git log`, o quite --commit para usar whisper.commit de node.yaml."))
	}
	req.Backend = strings.TrimSpace(c.String("backend"))
	if !contains(whisperBackends, req.Backend) {
		return req, c.usageErr(
			pick(c.Lang, "The backend "+quote(req.Backend)+" is not one of auto, cuda, vulkan or cpu.",
				"El backend "+quote(req.Backend)+" no es auto, cuda, vulkan ni cpu."),
			pick(c.Lang, "Use --backend auto, cuda, vulkan or cpu.", "Use --backend auto, cuda, vulkan o cpu."))
	}
	req.SourceDir = c.String("source")
	req.FromKit = c.Bool("from-kit")
	if req.SourceDir != "" && req.FromKit {
		return req, c.usageErr(
			pick(c.Lang, "--source and --from-kit cannot be used together.", "--source y --from-kit no se pueden usar juntos."),
			pick(c.Lang, "Choose one source: a folder (--source) or the rebuild kit (--from-kit).",
				"Elija una sola fuente: una carpeta (--source) o el kit de reconstrucción (--from-kit)."))
	}
	if c.Bool("activate") && c.Bool("no-activate") {
		return req, c.usageErr(
			pick(c.Lang, "--activate and --no-activate cannot be used together.", "--activate y --no-activate no se pueden usar juntos."),
			pick(c.Lang, "Use --no-activate to only build and test, or leave both out.", "Use --no-activate para solo compilar y probar, o quite ambos."))
	}
	req.NoActivate = c.Bool("no-activate")
	req.DryRun = c.Bool("dry-run")
	// The kit alone is the offline mode: nothing may use the network, and a commit the kit lacks is
	// LAI-KIT-002.
	req.Offline = req.FromKit
	return req, nil
}

// whisperPlanLine is "Building whisper.cpp 1a2b3c4d5 for cuda (sm 75), 5 parallel jobs".
func whisperPlanLine(l Lang, in build.Inputs, req build.Request) string {
	arch := ""
	if in.CudaArch != "" {
		arch = " (sm " + in.CudaArch + ")"
	}
	return fmt.Sprintf(pick(l, "Building whisper.cpp %s for %s%s, %d parallel jobs", "Compilando whisper.cpp %s para %s%s, %d trabajos en paralelo"),
		in.ShortCommit, in.Backend, arch, in.Jobs)
}

func whisperConfirmPrompt(l Lang, in build.Inputs, req build.Request) string {
	if req.NoActivate {
		return fmt.Sprintf(pick(l,
			"Build whisper.cpp %s for %s? This takes a while; the speech service is not restarted.",
			"¿Compilar whisper.cpp %s para %s? Tarda un rato; el servicio de voz no se reinicia."), in.ShortCommit, in.Backend)
	}
	return fmt.Sprintf(pick(l,
		"Build whisper.cpp %s for %s and switch the speech service to it? This takes a while.",
		"¿Compilar whisper.cpp %s para %s y cambiar el servicio de voz a esa versión? Tarda un rato."), in.ShortCommit, in.Backend)
}

// ---- --dry-run ----------------------------------------------------------------------------------

// whisperDryRun prints every check of the preflight and exits 0 only when all pass.
func whisperDryRun(c *Call, eng whisperEngine, req build.Request) error {
	res, err := eng.Build(c.Ctx, req)
	rep := res.Preflight
	if rep == nil {
		return whisperErr(c, err) // the request could not even be checked
	}
	if c.Global.JSON {
		if err != nil {
			return whisperErr(c, err)
		}
		return c.Out.JSON().Document(rep)
	}
	for _, ch := range rep.Checks {
		line := ch.Title + ": " + ch.Detail
		if ch.OK {
			c.Out.OK(line)
			continue
		}
		fix := ""
		if _, f, ok := doctor.Text(ch.Code, string(c.Lang)); ok {
			fix = sentence(f)
		}
		c.Out.Finding(out.Problem{Severity: sevError, Code: ch.Code, Message: sentence(line), Fix: fix})
	}
	if rep.InstallCommand != "" {
		c.Out.Info(pick(c.Lang, "To install what is missing from the kit, run: ", "Para instalar lo que falta desde el kit, ejecute: ") + "`" + rep.InstallCommand + "`")
	}
	if err != nil {
		c.Out.Blank()
		c.Out.Result(pick(c.Lang,
			"Not ready: fix the checks marked above and run `sudo aictl rebuild-whisper --dry-run` again.",
			"No está listo: corrija las comprobaciones marcadas arriba y ejecute `sudo aictl rebuild-whisper --dry-run` otra vez."))
		return ExitWith(ExitFailed)
	}
	c.Out.OK(pick(c.Lang, "Everything needed to rebuild whisper.cpp is in place.", "Todo lo necesario para recompilar whisper.cpp está listo."))
	return nil
}

// ---- the result ---------------------------------------------------------------------------------

func whisperDone(c *Call, res build.Result, req build.Request) error {
	if c.Global.JSON {
		return c.Out.JSON().Document(res)
	}
	for _, w := range res.Warnings {
		c.Out.Warn(w)
	}
	switch {
	case res.Switched:
		c.Out.OK(fmt.Sprintf(pick(c.Lang,
			"Build %s of whisper.cpp passed its test. Speech-to-text is running again.",
			"La versión %s de whisper.cpp superó su prueba. El reconocimiento de voz está en marcha otra vez."), res.BuildID))
	default:
		c.Out.OK(fmt.Sprintf(pick(c.Lang,
			"Build %s of whisper.cpp passed its test. It was not switched on (--no-activate); run `sudo aictl rebuild-whisper` to use it.",
			"La versión %s de whisper.cpp superó su prueba. No se activó (--no-activate); ejecute `sudo aictl rebuild-whisper` para usarla."), res.BuildID))
	}
	return nil
}

// whisperErr turns what the engine returns into the command's error: its doctor code, the engine's
// plain sentence (the code's own title in Spanish), the code's fix, and the log of a failed build.
// Errors that are not the engine's (a cancelled context) are returned as they are.
func whisperErr(c *Call, err error) error {
	var be *build.Error
	if !errors.As(err, &be) {
		return err
	}
	exit := ExitFailed
	if be.Code == doctor.AGT022 {
		exit = ExitRefused
	}
	e := c.codeError(be.Code, exit, "")
	if c.Lang != LangES || e.Message == "" {
		e.Message = be.Message
	}
	switch be.Code {
	case doctor.KIT020:
		e.Fix = pick(c.Lang, "Read the build log shown, then run `sudo aictl rebuild-whisper --dry-run`.",
			"Lea el registro de compilación mostrado y ejecute `sudo aictl rebuild-whisper --dry-run`.")
	case doctor.KIT002:
		e.Fix = pick(c.Lang, "Sync the kit with internet, or choose a commit that is in the kit.",
			"Sincronice el kit con internet, o elija una versión que esté en el kit.")
	}
	detail := []any{"stage", string(be.Stage)}
	for _, k := range []string{"cause", "build_id", "commit", "backend", "previous_build", "rolled_back", "log_path", "packages", "install_command", "failed_checks"} {
		if v, ok := be.Details[k]; ok {
			detail = append(detail, k, v)
		}
	}
	e = e.WithDetails(detail...).WithCause(be)
	if p, ok := be.Details["log_path"].(string); ok && p != "" && !c.Global.JSON {
		e.Message = strings.TrimSpace(e.Message) + " " + fmt.Sprintf(pick(c.Lang, "The log is in %s.", "El registro está en %s."), p)
	}
	return e
}

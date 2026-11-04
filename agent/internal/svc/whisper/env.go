package whisper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// envFileMode is the mode of whisper.env (root:localai 0640, companion-services.md section 4.2.2).
const envFileMode os.FileMode = 0o640

// MaxThreads caps threads: auto (companion-services.md section 3.1).
const MaxThreads = 8

// Device values of Selection.Device.
const (
	DeviceGPU = "gpu"
	DeviceCPU = "cpu"
)

// vramHeadroomMiB is the 1024 MiB the device rule leaves free on top of the survival model.
const vramHeadroomMiB = 1024

// Config is the whisper block of node.yaml, the keys SVC-06 reads.
type Config struct {
	Enabled *bool `yaml:"enabled"`
	// Backend is auto | cuda | vulkan | cpu.
	Backend string `yaml:"backend"`
	// Device is auto | gpu | cpu.
	Device string `yaml:"device"`
	// Model is auto or a catalog whisper model id.
	Model string `yaml:"model"`
	// Threads is auto or a number (a string so that both decode).
	Threads string `yaml:"threads"`
	// MaxAudioSeconds is the longest recording POST /speech/stt takes (0 means 120).
	MaxAudioSeconds int `yaml:"max_audio_seconds"`
}

// ConfigFromNode decodes the whisper block of node.yaml; an absent block gives the defaults.
func ConfigFromNode(cfg config.Config) Config {
	var c Config
	if raw, ok := cfg.Blocks["whisper"]; ok {
		_ = raw.Decode(&c)
	}
	return c
}

// Hardware is the machine side of the device rule, in MiB and cores.
type Hardware struct {
	// PhysicalCores are the physical cores (not logical CPUs); 0 means unknown.
	PhysicalCores int
	// TotalVRAMMiB is G_total of the fit rules for the node's backend (the GPU total on a
	// unified-memory node); 0 when there is no GPU.
	TotalVRAMMiB int64
	// FreeVRAMMiB is the graphics memory free now; negative means not measured.
	FreeVRAMMiB int64
}

// Inputs is everything Resolve decides from. It is a plain value; nothing in it is read from disk.
type Inputs struct {
	Config  Config
	Catalog *catalog.Catalog
	Profile *catalog.Profile
	// AddModels are node.yaml profile_overrides.add_models; they count as part of include.models.
	AddModels []string
	// Copies answers "which verified copy of this item do I use" (the best-copy index).
	Copies ark.CopyFinder
	// LlamaBackend is node.yaml llama.backend; "auto" and "" in whisper.backend mean this.
	LlamaBackend string
	// BuildBackend is the backend of the build `current` points at ("" when there is no build).
	BuildBackend string
	Hardware     Hardware
	// ForceCPU is the in-memory result of the start-failure fallback (fallback.go).
	ForceCPU bool
}

// Selection is the resolved speech setup: the content of whisper.env.
type Selection struct {
	ModelID   string
	ModelPath string
	Threads   int
	// Device is DeviceGPU or DeviceCPU.
	Device string
	// ReserveMiB is the graphics memory whisper reserves for the router fit rules (W_res): the
	// model's min_vram_mib on the gpu, 0 on the cpu.
	ReserveMiB int64
	// Backend is the resolved backend (never auto).
	Backend string
}

// DeviceArgs is the value of WHISPER_DEVICE_ARGS.
func (s Selection) DeviceArgs() string {
	if s.Device == DeviceCPU {
		return "--no-gpu"
	}
	return ""
}

// Problem is a finding of Resolve, carrying a doctor code.
type Problem struct {
	Code     string
	Severity doctor.Severity
	Details  map[string]any
}

// Resolve picks the model, the thread count and the device (companion-services.md sections 4.2.2 and
// 4.2.3). It is pure. When no model can be chosen the Selection has an empty ModelID and the problems
// hold LAI-SVC-010 (error); when the device ends up on the processor for lack of graphics memory the
// problems hold LAI-SVC-013 (info).
func Resolve(in Inputs) (Selection, []Problem) {
	var sel Selection
	var probs []Problem

	sel.Backend = in.Config.Backend
	if sel.Backend == "" || sel.Backend == "auto" {
		sel.Backend = in.LlamaBackend
	}
	sel.Threads = resolveThreads(in.Config.Threads, in.Hardware.PhysicalCores)

	m, path, why := selectModel(in)
	if m == nil {
		probs = append(probs, Problem{Code: doctor.SVC010, Severity: doctor.Error, Details: map[string]any{"reason": why, "model": in.Config.Model}})
		sel.Device = DeviceCPU
		return sel, probs
	}
	sel.ModelID, sel.ModelPath = m.ID, path

	dev, reason := resolveDevice(in, sel.Backend, m)
	sel.Device = dev
	if dev == DeviceGPU {
		sel.ReserveMiB = m.Requirements.MinVRAMMiB
	} else if reason != "" {
		probs = append(probs, Problem{Code: doctor.SVC013, Severity: doctor.Info, Details: map[string]any{"reason": reason}})
	}
	return sel, probs
}

func resolveThreads(setting string, physical int) int {
	setting = strings.TrimSpace(setting)
	if n, err := strconv.Atoi(setting); err == nil && n >= 1 {
		return n
	}
	t := physical / 2
	if t > MaxThreads {
		t = MaxThreads
	}
	if t < 1 {
		t = 1
	}
	return t
}

// selectModel applies section 4.2.2: auto is the first engine: whisper model of include.models (plus
// add_models) with a verified copy; an explicit id is that model or nothing.
func selectModel(in Inputs) (m *catalog.Model, path string, why string) {
	if in.Catalog == nil || in.Copies == nil {
		return nil, "", "no_catalog"
	}
	try := func(id string) (*catalog.Model, string, bool) {
		cm, ok := in.Catalog.Model(id)
		if !ok || cm.Engine != "whisper" {
			return nil, "", false
		}
		p := modelFile(in.Copies, cm)
		return cm, p, p != ""
	}
	if id := strings.TrimSpace(in.Config.Model); id != "" && id != "auto" {
		cm, p, ok := try(id)
		if !ok {
			if cm == nil {
				return nil, "", "unknown_model"
			}
			return nil, "", "not_installed"
		}
		return cm, p, ""
	}
	var ids []string
	if in.Profile != nil {
		ids = append(ids, in.Profile.Include.Models...)
	}
	ids = append(ids, in.AddModels...)
	for _, id := range ids {
		if cm, p, ok := try(id); ok {
			return cm, p, ""
		}
	}
	return nil, "", "not_installed"
}

// modelFile is the verified whisper file (files[kind=whisper]) of the best copy, "" when there is none.
func modelFile(copies ark.CopyFinder, m *catalog.Model) string {
	cp, err := copies.BestCopy(m.ID)
	if err != nil {
		return ""
	}
	for _, f := range m.Files {
		if f.Kind != "whisper" {
			continue
		}
		for _, p := range cp.Files {
			if filepath.Base(p) == f.Name {
				return p
			}
		}
	}
	return ""
}

// resolveDevice applies section 4.2.3. The second result is why the processor was chosen, "" when the
// operator asked for it (an explicit device: cpu or backend: cpu raises nothing).
func resolveDevice(in Inputs, backend string, m *catalog.Model) (device, reason string) {
	if in.ForceCPU {
		return DeviceCPU, "start_failures"
	}
	switch in.Config.Device {
	case DeviceCPU:
		return DeviceCPU, ""
	case DeviceGPU:
		return DeviceGPU, ""
	}
	if backend == "cpu" || backend == "" {
		return DeviceCPU, ""
	}
	if in.BuildBackend == "cpu" {
		return DeviceCPU, "cpu_build"
	}
	need := m.Requirements.MinVRAMMiB
	var survival int64
	if in.Profile != nil && in.Catalog != nil && len(in.Profile.Survival.Models) > 0 {
		if sm, ok := in.Catalog.Model(in.Profile.Survival.Models[0]); ok {
			survival = sm.Requirements.MinVRAMMiB
		}
	}
	if in.Hardware.TotalVRAMMiB-survival-vramHeadroomMiB < need {
		return DeviceCPU, "vram_total"
	}
	if in.Hardware.FreeVRAMMiB >= 0 && in.Hardware.FreeVRAMMiB < need {
		return DeviceCPU, "vram_free"
	}
	return DeviceGPU, ""
}

// CurrentBuildBackend is the backend of the build the `current` link of root points at
// ("<commit9>-<backend>"), "" when there is no link or its name has no backend. root "" means Root.
func CurrentBuildBackend(root string) string {
	if root == "" {
		root = Root
	}
	target, err := os.Readlink(filepath.Join(root, "current"))
	if err != nil {
		return ""
	}
	return buildBackend(target)
}

// RenderEnv returns the text of whisper.env (companion-services.md section 3.2). It is deterministic.
func RenderEnv(s Selection) []byte {
	var b bytes.Buffer
	b.WriteString("# Generated by aiagent. Do not edit. Regenerated on model/drive changes.\n")
	fmt.Fprintf(&b, "WHISPER_MODEL=%s\n", s.ModelPath)
	fmt.Fprintf(&b, "WHISPER_THREADS=%d\n", s.Threads)
	fmt.Fprintf(&b, "WHISPER_DEVICE_ARGS=%s\n", s.DeviceArgs())
	fmt.Fprintf(&b, "WHISPER_MODEL_ID=%s\n", s.ModelID)
	return b.Bytes()
}

// Restarter restarts and stops the speech unit; internal/services.Manager implements it.
type Restarter interface {
	Restart(ctx context.Context, unit string) error
	Stop(ctx context.Context, unit string) error
}

// EnvWriter writes whisper.env and restarts the unit when the content changed.
type EnvWriter struct {
	// Path is the env file; default EnvPath.
	Path string
	// FS is the disk; default config.OSFS.
	FS config.FS
	// UID and GID own the file (root:localai); -1 leaves an id alone.
	UID, GID int
	// Units restarts or stops localai-whisper; required.
	Units Restarter
}

// EnvResult is what one Apply did.
type EnvResult struct {
	// Written is true when the file's bytes changed.
	Written bool
	// Restarted is true when the unit was restarted after a change.
	Restarted bool
	// Stopped is true when the unit was stopped because no model is installed.
	Stopped bool
}

// Apply makes the node match sel. With a model it writes whisper.env (tmp + rename, 0640) when the
// bytes differ and then restarts the unit, exactly once; unchanged bytes touch nothing. With no model
// (empty ModelID) it stops the unit and leaves the file alone. A restart or stop that fails is
// returned with the result.
func (w *EnvWriter) Apply(ctx context.Context, sel Selection) (EnvResult, error) {
	var res EnvResult
	if w.Units == nil {
		return res, errors.New("whisper: the env writer needs Units")
	}
	if sel.ModelID == "" {
		if err := w.Units.Stop(ctx, UnitName); err != nil {
			return res, fmt.Errorf("whisper: stop %s with no model: %w", UnitName, err)
		}
		res.Stopped = true
		return res, nil
	}
	path := w.Path
	if path == "" {
		path = EnvPath
	}
	fsys := w.FS
	if fsys == nil {
		fsys = config.OSFS{}
	}
	data := RenderEnv(sel)
	cur, rerr := fsys.ReadFile(path)
	if rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		return res, fmt.Errorf("whisper: read %s: %w", path, rerr)
	}
	if rerr == nil && bytes.Equal(cur, data) {
		return res, nil
	}
	if err := config.WriteFileAtomicFS(fsys, path, data, envFileMode, w.UID, w.GID); err != nil && !errors.Is(err, config.ErrNotDurable) {
		return res, fmt.Errorf("whisper: write %s: %w", path, err)
	}
	res.Written = true
	if err := w.Units.Restart(ctx, UnitName); err != nil {
		return res, fmt.Errorf("whisper: restart %s after writing %s: %w", UnitName, path, err)
	}
	res.Restarted = true
	return res, nil
}

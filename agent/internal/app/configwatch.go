package app

import (
	"context"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/logx"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/svc/kiwix"
)

// watchConfig applies the live settings of node.yaml that need more than their owner re-reading the
// store (agent.md section 3.4, "Live"): logging.level, kiwix.threads (the unit is rewritten and
// systemd reloaded, never restarted) and the settings that change models.ini (profile, overrides and
// llama.legacy_aliases), which are regenerated when the file was edited on disk. A change made through
// Store.Update (PUT /profile) regenerates in its own handler, so it is not done twice here.
func (d *Daemon) watchConfig(ctx context.Context) {
	ch, cancel := d.c.Config.Subscribe()
	d.bg.Add(1)
	go func() {
		defer d.bg.Done()
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case c, ok := <-ch:
				if !ok {
					return
				}
				d.applyConfigChange(ctx, c)
			}
		}
	}()
}

func (d *Daemon) applyConfigChange(ctx context.Context, c config.Change) {
	if hasPath(c.Changed, "logging.level") && !c.New.SafeDefaults {
		if err := logx.SetLevel(d.level, c.New.Logging.Level); err != nil {
			d.log.Warn("logging.level is not valid", "level", c.New.Logging.Level)
		}
	}
	if hasPath(c.Changed, "kiwix") && kiwixThreads(c.Old) != kiwixThreads(c.New) && blockEnabled(c.New, "kiwix") {
		d.writeKiwixUnit(ctx, kiwixThreads(c.New))
	}
	if hasPrefix(c.Changed, "whisper") && blockEnabled(c.New, "whisper") {
		d.writeWhisperUnit(ctx) // never starts or stops the unit; applyWhisper restarts it when whisper.env changed
		d.applyWhisperAsync(ctx)
	}
	if hasPrefix(c.Changed, "sync") {
		d.ark.applySettings() // the bandwidth limit moves the shared bucket at once; the rest is read per run
	}
	if hasPath(c.Changed, "llama.mode") {
		d.applyLlamaMode(ctx)
	}
	if c.Source == config.SourceReload && (hasPrefix(c.Changed, "profile") || hasPath(c.Changed, "llama.legacy_aliases") || hasPrefix(c.Changed, "catalog")) {
		if _, err := d.sdeps.Regenerator.Regenerate(ctx); err != nil {
			d.log.Warn("models.ini was not regenerated after node.yaml changed", "error", err.Error())
		}
	}
}

// applyLlamaMode switches llama between router and single-model mode when node.yaml llama.mode
// changed (llama-router.md section 4.8): the orchestrator regenerates the unit, daemon-reloads,
// restarts llama and restores the default, under modelOpsMu. It runs in the background: it can take as
// long as a model load, and a refused or failed switch is reported by its log line and the doctor.
//
// The returned channel is closed when the switch has ended (tests wait on it).
func (d *Daemon) applyLlamaMode(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	d.bg.Add(1)
	go func() {
		defer d.bg.Done()
		defer close(done)
		res, err := d.c.Orchestrator.ApplyMode(ctx, router.ModeRequest{LockWait: router.DefaultLockWait})
		if err != nil {
			d.log.Warn("llama.mode changed but the AI service was not switched", "error", err.Error())
			return
		}
		d.log.Info("llama.mode applied", "mode", res.Mode, "restarted", res.Changed)
	}()
	return done
}

// writeKiwixUnit rewrites localai-kiwix.service for threads and reloads systemd. The writer never
// starts, stops or restarts the unit; the new thread count applies at its next start.
func (d *Daemon) writeKiwixUnit(ctx context.Context, threads int) {
	w := &kiwix.UnitWriter{Path: d.c.Paths.KiwixUnit(), Systemd: d.c.Systemd}
	if d.env.Unprivileged || !d.haveLocalAI {
		w.UID, w.GID = -1, -1
	}
	res, err := w.Write(ctx, threads)
	if err != nil {
		d.log.Error("the kiwix unit could not be rewritten", "threads", threads, "error", err.Error())
		return
	}
	d.log.Info("the kiwix unit was rewritten", "threads", threads, "written", res.Written)
}

func hasPath(paths []string, p string) bool {
	for _, x := range paths {
		if x == p {
			return true
		}
	}
	return false
}

func hasPrefix(paths []string, prefix string) bool {
	for _, x := range paths {
		if x == prefix || strings.HasPrefix(x, prefix+".") || strings.HasPrefix(x, prefix+"_") {
			return true
		}
	}
	return false
}

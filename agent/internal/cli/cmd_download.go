package cli

import (
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
	"github.com/marinoscar/local-ai/agent/internal/cli/progress"
	agentevents "github.com/marinoscar/local-ai/agent/internal/events"
)

// `aictl download <model-id> [--root <path>] [--wait] [--allow-offline-mode]` (aictl.md section
// 4.6): POST /models/{id}/download hands the model to the sync engine, which answers 202 with the
// sync run that does it. Without --wait the command ends there; with --wait it follows that run's
// sync.* events until sync.done, like `aictl ark sync --wait`.
//
// Online mode only: in offline mode the agent wants --allow-offline-mode (409 LAI-AGT-023) and in
// survival mode it refuses (409 LAI-SYN-010); both are exit 6 and nothing is queued.

// downloadQueued is the 202 body of POST /models/{id}/download (agent.md section 3.8.11).
type downloadQueued struct {
	ID        string `json:"id"`
	SyncRunID string `json:"sync_run_id"`
	Queued    bool   `json:"queued"`
}

// downloadRequestTimeout is the deadline of the request that queues the download. Waiting for the
// run has none (the download takes as long as the network does).
const downloadRequestTimeout = 30 * time.Second

func init() {
	Register(&Command{
		Name:    "download",
		Summary: "Download a model from the internet (online mode).",
		Usage:   "<model-id> [--root <path>] [--wait] [--allow-offline-mode]",
		Description: `Asks the sync engine to fetch the model into the content folder (or the drive named with
--root; a path or a root id from aictl ark drives, repeatable). Without --wait it returns at once: follow it
with aictl ark status. With --wait it shows the progress and ends when the download is done.
In offline mode it needs --allow-offline-mode (it uses whatever network exists). In survival mode it is
refused. --json prints the 202 body, or with --wait the final sync.done data.`,
		Timeout: 0,
		Flags: func(fs *flag.FlagSet) {
			fs.Var(&multiFlag{}, "root", "content root to download into: a path or a root id (repeatable)")
			fs.Bool("wait", false, "wait until the download is done")
			fs.Bool("allow-offline-mode", false, "download in offline mode too (uses whatever network exists)")
		},
		Run: runDownload,
	})
}

func runDownload(c *Call) error {
	id, err := c.modelIDArg()
	if err != nil {
		return err
	}
	roots := c.multi("root")
	for _, r := range roots {
		if err := c.checkRoot("--root", r, true); err != nil {
			return err
		}
	}
	cl, err := c.Client()
	if err != nil {
		return err
	}
	body := map[string]any{"allow_offline_mode": c.Bool("allow-offline-mode")}
	if len(roots) > 0 {
		body["roots"] = roots
	}
	post := func(ctx context.Context) (downloadQueued, json.RawMessage, error) {
		var q downloadQueued
		raw, _, err := c.callWith(cl, ctx, http.MethodPost, modelPath(id, "/download"), body, &q)
		return q, raw, err
	}
	detail := c.modelDetail(cl, id)
	name := modelLabel(detail, id)
	size := ""
	if detail != nil && detail.SizeBytes > 0 {
		size = " (" + sizeText(detail.SizeBytes) + ")"
	}

	if !c.Bool("wait") {
		ctx, cancel := c.withDefaultTimeout(downloadRequestTimeout)
		defer cancel()
		_, raw, err := post(ctx)
		if err != nil {
			return err
		}
		if c.Global.JSON {
			return c.emitRaw(raw)
		}
		c.Out.OK(pick(c.Lang,
			"Download of "+name+size+" has started. Follow it with `aictl ark status`.",
			"Empezó la descarga de "+name+size+". Sígala con `aictl ark status`."))
		return nil
	}

	var runID string
	outcomes := map[string]*itemOutcome{}
	outcome := func(item string) *itemOutcome {
		o := outcomes[item]
		if o == nil {
			o = &itemOutcome{}
			outcomes[item] = o
		}
		return o
	}
	res, ferr := c.Follow(FollowSpec{
		Progress: progress.Spec{
			Types:    []string{"sync.*"},
			AwaitKey: true,
			Match: func(ev progress.Event, key string) bool {
				var d struct {
					RunID string `json:"run_id"`
				}
				return key != "" && (ev.OpID == key || json.Unmarshal(ev.Data, &d) == nil && d.RunID == key)
			},
			OnEvent: func(ev progress.Event) progress.Step {
				switch ev.Type {
				case agentevents.TypeSyncProgress:
					var d agentevents.SyncProgress
					if json.Unmarshal(ev.Data, &d) != nil {
						return progress.Step{}
					}
					outcome(d.Item).progressed = true
					u := c.arkProgressUpdate(kindSync, d)
					if d.Item == id {
						u.Label = strings.Replace(u.Label, d.Item, name, 1)
					}
					return progress.Step{Update: u}
				case agentevents.TypeSyncItemDone:
					var d agentevents.SyncItemDone
					if json.Unmarshal(ev.Data, &d) == nil {
						o := outcome(d.Item)
						o.result, o.reason, o.code = d.Result, d.Reason, d.Code
					}
				case agentevents.TypeSyncDone:
					var d agentevents.SyncDone
					if json.Unmarshal(ev.Data, &d) != nil {
						return progress.Step{}
					}
					return progress.Step{Done: true, Result: runDoc{RunID: d.RunID, State: d.State, Totals: d.Totals, Ready: d.Ready}}
				}
				return progress.Step{}
			},
			// GET /ark/sync is the truth after a lost stream: the run may have ended while we were away.
			Reread: func(ctx context.Context) (progress.Step, error) {
				if runID == "" {
					return progress.Step{}, nil
				}
				var r runDoc
				if _, _, err := c.callWith(cl, ctx, http.MethodGet, "/ark/sync", nil, &r); err != nil {
					return progress.Step{}, err
				}
				switch r.State {
				case "done", "cancelled", "failed", "skipped":
					if r.RunID == runID {
						return progress.Step{Done: true, Result: r}, nil
					}
				}
				return progress.Step{}, nil
			},
		},
		Request: func(ctx context.Context) (string, error) {
			rctx, cancel := context.WithTimeout(ctx, downloadRequestTimeout)
			defer cancel()
			q, _, err := post(rctx)
			if err != nil {
				return "", err
			}
			runID = q.SyncRunID
			return q.SyncRunID, nil
		},
	})
	if ferr != nil {
		return ferr
	}
	doc, _ := res.Value.(runDoc)
	return c.downloadFinished(id, name, size, doc, outcomes)
}

// downloadFinished prints how a followed download ended. The model is there when its item was
// verified; anything else is a failure (exit 1) with the item's own code and fix.
func (c *Call) downloadFinished(id, name, size string, doc runDoc, outcomes map[string]*itemOutcome) error {
	if c.Global.JSON {
		if err := c.Out.JSON().Document(doc); err != nil {
			return err
		}
	}
	o := outcomes[id]
	// The item's own result says it; a run that ended "done" without one (its events were missed)
	// counts when it reports no failed and no skipped item.
	verified := false
	if doc.State == "done" {
		if o != nil && o.result != "" {
			verified = o.result == "verified"
		} else {
			t := doc.totals()
			verified = t.Failed == 0 && t.Skipped == 0
		}
	}
	if verified {
		c.Out.OK(pick(c.Lang, name+size+" was downloaded.", "Se descargó "+name+size+"."))
		return nil
	}
	if !c.Global.JSON {
		switch {
		case doc.State == "cancelled":
			c.Out.Info(pick(c.Lang,
				"The download was stopped. What was already downloaded is kept and continues next time.",
				"La descarga se detuvo. Lo ya descargado se conserva y continúa la próxima vez."))
		case doc.State == "skipped":
			c.Out.Info(pick(c.Lang, "The download did not start: "+orText(doc.Reason, "skipped")+".",
				"La descarga no empezó: "+orText(doc.Reason, "omitida")+"."))
		default:
			if o != nil && (o.result == "failed" || o.result == "quarantined" || o.result == "skipped") {
				c.itemProblems(map[string]*itemOutcome{id: o})
			}
			c.Out.Problem(out.Problem{Severity: out.SevError, Message: pick(c.Lang,
				"The download of "+name+" did not finish. Run `aictl doctor` to see why.",
				"La descarga de "+name+" no terminó. Ejecute `aictl doctor` para ver por qué.")})
		}
	}
	return ExitWith(ExitFailed)
}

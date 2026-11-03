package app

import (
	"os"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/docsrender"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/svc/whisper"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/sync/resolve"
	"github.com/marinoscar/local-ai/agent/internal/sync/run"
)

// This file wires the kit and docs source types of ark sync (ark-sync.md sections 4.9.6 to 4.9.9):
// git_mirror, apt_closure and repo_docs. Each producer is registered twice, as the planner's resolver
// (plan.Resolvers, the version and the size) and as the engine's task resolver (run.ItemResolvers, the
// fetch), over one resolve.Env. The producers hold all the behaviour; this is only the binding of their
// seams to the node: the runner, the downloader, the bandwidth bucket, the network state, the sync
// directory, the pins and the inventory.

// sourceResolveTimeout bounds one planner resolver call. The planner's own default (5 s) is meant for
// a metadata read; apt_closure computes a whole package closure (apt-get update and --print-uris, up
// to 25 minutes by its own bounds) and git ls-remote may be slow on a poor link, so a plan made with
// the default would leave them unresolved and a run would skip them. When the run may not use the
// network no resolver is called: the planner uses what the catalog pins and what a root already
// holds (plan.LocalResolver), and a producer refuses before it touches anything (Env.Online).
const sourceResolveTimeout = 30 * time.Minute

// aptSandboxUser is the user apt runs as (APT::Sandbox::User): the service user.
const aptSandboxUser = "localai"

// arkSources are the three producers of the kit and docs source types, kept on the stack so a test can
// reach them.
type arkSources struct {
	env  *resolve.Env
	git  *resolve.GitMirror
	apt  *resolve.AptClosure
	docs *resolve.RepoDocs
}

// registerSources builds the producers and registers them on the planner's and the engine's
// registries. dl is the sync downloader (the debs and the docs tarball).
func (s *arkStack) registerSources(dl *download.Downloader) error {
	d := s.d
	env := &resolve.Env{
		Runner:     d.env.Runner,
		Lookup:     d.env.Lookup,
		Downloader: dl,
		Bucket:     s.bucket,
		Online:     d.sourcesOnline,
		Now:        d.env.Clock.Now,
		Logger:     d.log.With("component", "sync"),
		SyncHome:   d.syncDir(),
	}
	src := &arkSources{env: env}
	src.git = &resolve.GitMirror{Env: env, NodeCommits: s.facts.mirrorCommits}

	// apt never runs as root: the service user when there is one. An unprivileged development run has
	// no Credential and runs apt as the agent's own user (a root agent without the service user is
	// refused by the producer itself, LAI-SYN-060). The keyrings of the extra repositories are not
	// embedded in the agent yet (rebuild-kit.md section 3.4), so only the Ubuntu archive keyring is
	// read, from the build machine's /usr/share/keyrings. What happens to an extra repository without
	// a keyring is node.yaml sync.apt_unverified_repos (allow by default, issue #274), read at the
	// start of every closure so a change of node.yaml applies to the next run.
	src.apt = &resolve.AptClosure{
		Env: env, SystemKeyringDir: d.c.Paths.Join("/usr/share/keyrings"), Euid: d.env.Geteuid, User: aptSandboxUser,
		AllowUnverifiedRepos: func() bool { return d.syncSettings().AllowUnverifiedAptRepos() },
	}
	if d.haveLocalAI && !d.env.Unprivileged {
		src.apt.Credential = &execx.Credential{UID: uint32(d.localaiUID), GID: uint32(d.localaiGID)}
	}

	host, _ := os.Hostname()
	src.docs = &resolve.RepoDocs{
		Env:            env,
		CatalogVersion: func() string { return d.c.Catalog.Get().Catalog.Version },
		Inventory:      s.inventory,
		State: docsrender.StateSources{
			NodeYAML:     d.c.Paths.NodeYAML(),
			InstallState: d.c.Paths.Join(varDir + "/install/state.json"),
			ContentRoots: []string{d.c.Paths.InternalRoot()},
			CatalogDirs:  []string{d.c.Paths.Catalog(), d.c.Paths.Join(internalDir + "/kit/catalog")},
		},
		Hostname: host,
	}
	if d.haveLocalAI && !d.env.Unprivileged {
		src.docs.Owner = &docsrender.Owner{UID: d.localaiUID, GID: d.localaiGID}
	}

	if err := resolve.Register(s.resolvers, src.git, src.apt, src.docs); err != nil {
		return err
	}
	for _, p := range []struct {
		sourceType string
		producer   resolve.Producer
	}{{"git_mirror", src.git}, {"apt_closure", src.apt}, {"repo_docs", src.docs}} {
		if err := run.RegisterProducer(s.itemResolvers, p.sourceType, p.producer); err != nil {
			return err
		}
	}
	s.sources = src
	return nil
}

// sourcesOnline is resolve.Env.Online, the second lock in front of every network operation of a
// producer: the internet is reachable and the node is not in survival mode. The first lock is the
// engine's preflight, which refuses a network run in offline mode unless a person allowed it
// (allow_offline_mode), so a run that got this far in offline mode is one that was allowed.
func (d *Daemon) sourcesOnline() bool {
	n := d.netState()
	return n.Online && n.Mode != string(plan.ModeSurvival)
}

// inventory is the path of inventory.local.yaml for the manuals: /etc/localai first, then the mount
// point of a mounted drive; "" when there is none (the manuals then keep their placeholders and the
// notice LAI-SYN-062 is raised).
func (s *arkStack) inventory() string {
	var drives []string
	for _, r := range s.roots.List() {
		if r.Kind == ark.KindArkDrive && r.Mount != "" {
			drives = append(drives, r.Mount)
		}
	}
	return docsrender.FindInventory(docsrender.InventoryLookup{EtcDir: s.d.c.Paths.Etc(), DriveRoots: drives})
}

// mirrorCommits are the commits this node itself pins for a git mirror (GitMirror.NodeCommits): the
// commit of the build in use. node.yaml keeps only the first characters of llama.commit, and a pin
// must be 40 hex digits, so the full commit comes from the BUILD.json of the build (the same source
// the kit pins use); a node.yaml value that is already complete is taken too. A value that is not
// 40 lowercase hex digits is left out rather than failing the mirror.
func (f *nodeFacts) mirrorCommits(mirror string) []string {
	cfg := f.d.c.Config.Get()
	var cands []string
	switch mirror {
	case "llama.cpp":
		cands = append(cands, buildCommit(f.d.c.Paths.BuildJSON()), cfg.Llama.Commit)
	case "whisper.cpp":
		cands = append(cands, buildCommit(f.d.c.Paths.Join(whisper.Root+"/current/BUILD.json")))
		if raw, ok := cfg.Blocks["whisper"]; ok {
			var w struct {
				Commit string `yaml:"commit"`
			}
			if raw.Decode(&w) == nil {
				cands = append(cands, w.Commit)
			}
		}
	}
	var out []string
	for _, c := range cands {
		if c = strings.TrimSpace(c); fullCommit.MatchString(c) {
			out = append(out, c)
		}
	}
	return out
}

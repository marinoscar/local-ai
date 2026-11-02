# AGENTS.md: rules for every agent and human working in this repo

This repository is the **runbook and source tree for the Local AI lab and the offline "ark"**. It has two jobs:

1. **Lab operation.** Run local AI on GPU nodes (coding, chat, vision, agentic work) and control all of them from `https://ai.marin.cr`.
2. **Doomsday ark.** Every node, especially the laptops, must be able to run **fully standalone with no internet**, with models, knowledge, maps, speech and a rebuild kit already on disk. A non-expert must be able to operate it from the `ai-desktop` app.

If a change makes (2) less reliable, it is wrong, even when it makes (1) nicer.

Start with [`docs/README.md`](docs/README.md) (map of all docs) and [`docs/architecture/overview.md`](docs/architecture/overview.md).

---

## MANDATORY rules

### 1. Issue first, always
- All work is tracked as **GitHub Issues** under an **Epic** in `marinoscar/local-ai`. Before writing code or docs, find the issue. If there is none, create it with the right template (`gh issue create --template feature_request.yml|bug_report.yml|runbook.yml|epic.yml`) and link it to its epic.
- **One issue = one branch = one PR.** The branch name is `<type>/<issue#>-<short-slug>`, for example `feat/23-router-ini-generator` or `docs/41-survival-runbook`. The PR body contains `Closes #<issue>`.
- Commits use **Conventional Commits** (`feat(agent): …`, `fix(sync): …`, `docs(runbook): …`). Keep them small, and make each one carry its own tests.
- Never push to `main` directly except for repo bootstrap work the owner explicitly approved.
- Every issue body links the **spec section(s)** that govern it. If the spec is wrong or missing something, fix the spec **in the same PR** and say so in the PR description. Specs and code must never disagree on `main`.

### 2. The repo is PUBLIC: never commit secrets or real lab inventory
- **Never commit:** API keys, `nod_`/`pat_`/`lai_` tokens, the agent token, the PIN hash, tailnet IPs (`100.x.y.z`), the tailnet name, the VPS public IP, the VPS hostname, Route53 zone IDs, LAN IPs, MAC/serial numbers, or email addresses of real people.
- Use the placeholders defined in [`inventory.example.yaml`](inventory.example.yaml), such as `<TAILNET_IP_AI_N1>`, `<VPS_PUBLIC_IP>`, `<TAILNET>` and `<ROUTE53_ZONE_ID>`. The real values live only in `inventory.local.yaml`, which is git-ignored and exists only on the owner's machines and the ark drive.
- Public hostnames (`ai.marin.cr`, `ai-n1.marin.cr`, …) and machine names (`hp-linux`, `msi-linux`, `ai-marin-linux`) may be committed.
- Run `scripts/check-redaction.sh` before every commit. CI runs it too, along with gitleaks. See [`docs/architecture/security-model.md`](docs/architecture/security-model.md) §Redaction.
- In examples, use placeholders for people and orgs (`Person A`, `Org Alpha`). Never use real names.

### 3. Offline first
- No component may **require** the internet at runtime except the cloud app, the gateway and ark sync. These are the only parts allowed to talk to the internet. Every other node feature must work with the network cable pulled and Tailscale down.
- No runtime downloads from CDNs: no web fonts, no JS from unpkg, no map tiles from the internet. Every asset the local UI, the Survival Portal or the desktop app needs is **embedded or on local disk**.
- Anything a non-expert needs during an emergency must be reachable from the **Survival Portal** (`http://127.0.0.1:7070`), and must also be described in the printable runbook [`docs/runbooks/01-survival-mode.md`](docs/runbooks/01-survival-mode.md).
- Destructive operations, such as deleting a model or content, need explicit confirmation. In the desktop UI they also need the admin PIN. Sync must **never** delete the last verified copy of anything.

### 4. Plain language for operators
- Runbooks are written for someone with **very little technical expertise**. Number the steps. Give the exact command or the exact button. Show "What you should see" after each step, and say "If it doesn't work…" with a link to the troubleshooting runbook.
- Every error a user can see carries a **doctor code** (`LAI-XXX-NNN`, see [`docs/specs/doctor-codes.md`](docs/specs/doctor-codes.md)) and a one-sentence fix.

### 5. Single sources of truth
| Topic | Source of truth |
|---|---|
| Paths, ports, unit names, users/groups | [`docs/architecture/overview.md`](docs/architecture/overview.md) §Canonical names |
| Agent REST API | [`docs/specs/agent.md`](docs/specs/agent.md) (OpenAPI generated from code must match) |
| CLI | [`docs/specs/aictl.md`](docs/specs/aictl.md) |
| Catalog / profile YAML schemas | [`docs/specs/catalog.md`](docs/specs/catalog.md) |
| Modes state machine | [`docs/architecture/modes.md`](docs/architecture/modes.md) |
| Error codes | [`docs/specs/doctor-codes.md`](docs/specs/doctor-codes.md) |
| Decisions | [`docs/decisions/`](docs/decisions/) ADRs. Changing a decision means writing a new ADR that supersedes the old one |

Do not restate these values elsewhere. Link to them. Runbooks may show concrete commands, but the commands must agree with the specs.

---

## Repo layout
```
AGENTS.md / CLAUDE.md       these rules
inventory.example.yaml      schema + placeholders for real lab values (real file: inventory.local.yaml, git-ignored)
docs/                       all documentation (see docs/README.md)
  architecture/             overview, modes, security model, lab inventory (redacted)
  decisions/                ADRs
  specs/                    component specs, the contract code is built against
  runbooks/                 plain-language operator runbooks
  source/                   redacted archive of the original gists/infra docs (read-only history)
  ROADMAP.md                epics, issue map, dependency graph
catalog/                    models.yaml, ark.yaml, profiles/*.yaml (data, validated by the agent)
agent/                      Go module: aiagent daemon + aictl CLI + embedded local UI   (E1–E5)
installers/                 install-node.sh, offline installer                              (E5)
packages/ui/                shared React components (local UI, cloud, desktop)             (E4/E6/E8)
apps/cloud/                 ai.marin.cr, created from EnterpriseAppBase starter             (E6/E7)
apps/desktop/               ai-desktop Electron shell                                       (E8)
scripts/                    repo tooling (redaction check, doc rendering)
```

## Local setup (once per clone)

1. **Install the pre-commit hook** (blocks commits that leak lab inventory or secrets):
   `scripts/install-hooks.sh`. It is idempotent: run it again any time. It makes git run
   `scripts/check-redaction.sh --staged` before every commit.
2. **Create your private inventory** (owner machines only): `cp inventory.example.yaml inventory.local.yaml`
   and fill in the real values. It is git-ignored. When it exists, `check-redaction.sh` also
   scans for its literal values (see issue #135).
3. **Install gitleaks** (optional locally, always run in CI) to reproduce the CI secret scan:
   - macOS: `brew install gitleaks`
   - Linux or anywhere with Go: `go install github.com/gitleaks/gitleaks/v8@latest`
   - or download a release binary from <https://github.com/gitleaks/gitleaks/releases>

   Then run `gitleaks detect --config .gitleaks.toml --redact` from the repo root.
4. **Check before you push:** `scripts/check-redaction.sh` and `scripts/tests/test-redaction.sh`.

The redaction check never prints a matched value: only `LEAK <file>:<line>`. Test fixtures with
deliberately fake leaks live in `scripts/tests/redaction/`, which `check-redaction.sh` skips only
while the marker file `scripts/tests/redaction/.redaction-fixtures` exists there.

## Tech stack (decided, see ADRs)
- **Agent + CLI:** Go (latest stable), one static binary (`CGO_ENABLED=0`), built for linux/amd64 and linux/arm64. It uses the standard library `net/http`, `log/slog` for structured JSON logs, and `gopkg.in/yaml.v3`. Keep dependencies minimal and vendored (`go mod vendor`) so the kit can rebuild the agent offline. See ADR-0001.
- **Local UI / Survival Portal:** React + Vite, built to static files and embedded with `go:embed`. It must not load any network assets.
- **Cloud:** EnterpriseAppBase packages (NestJS 11/Fastify, Prisma 7, Postgres 16, React 19 + MUI). See ADR-0008.
- **Desktop:** Electron + electron-builder, following the kvox-desktop pattern, as a thin shell. See ADR-0004.
- **Inference:** llama.cpp `llama-server` in **router mode**. See ADR-0002.

## Testing expectations
- Go: `go test ./...` must pass. Every spec'd state machine (sync, modes, router load) gets table-driven tests, including failure paths: checksum mismatch, disk full, a killed process.
- Anything that touches systemd, the GPU or the network gets a **fake** behind an interface for unit tests, plus a documented manual test in the issue's test plan.
- Runbook changes are dry-run by following them literally on a node. Record the result in the PR.

## Working style for coding agents
- Read the issue, the linked spec sections and the relevant ADRs **before** writing code.
- If anything in the issue is ambiguous, prefer the spec. If the spec is silent, choose the option that keeps offline operation safest, and record the decision in the spec in the same PR.
- Do not invent new paths, ports or unit names. Use the canonical ones, or add new ones to the canonical table first.
- Do not run state-changing commands on a production node (`systemctl restart`, `tailscale serve`, rotating keys) unless the issue's test plan says to, and the owner is running it.

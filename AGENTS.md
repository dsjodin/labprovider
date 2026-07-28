# AGENTS.md

This is the single source of implementation rules and architectural boundaries.
Operator-facing behavior is documented in `README.md`; pre-implementation design
notes for features that now exist are kept as history in `docs/design/`, and
reviews and plans in `docs/`.

## Purpose

This repository implements "labprovider": a single-node platform of shared infrastructure services for lab and PoC environments, especially VMware Cloud Foundation (VCF). All services run as Docker Compose stacks, deployed by a Go control plane (`services/control-plane`) that serves a web UI: configuration wizard, service selection + deployment with live progress, and a read-only dashboard.

Agents must preserve:
- simplicity
- reproducibility
- readability
- strict validation
- single-node design

---

## Core Principles

- Single-node, lab-oriented design only
- Explicit Go deployers over abstraction; one file per service under `services/control-plane/internal/deploy/`
- Template-driven configuration (Go text/template, embedded in the binary, `missingkey=error`)
- Fail fast on invalid input; the wizard reports every validation finding at once
- Keep implementations simple and easily understandable
- Prefer reproducibility over flexibility or convenience
- This repository provides infrastructure primitives only, not user-facing application platforms

---

## Architecture (v2)

- `install.sh` is the only shell: Docker install, one-time host prep (systemd-resolved stub listener, systemd-timesyncd), control-plane image build + run. Everything else is Go.
- The control plane runs as a root, host-networked, non-privileged container with the docker socket, `/opt/labprovider`, and `/host/etc` mounted. It execs the bundled docker CLI (compose v2) against the host daemon.
- The deploy engine (`internal/deploy`) is a static registry of services with explicit dependencies, executed sequentially in dependency order, single-flight, streaming progress over SSE.
- Configuration is the single flat `labprovider.env`, managed at `/opt/labprovider/control-plane/labprovider.env` by the wizard; the shipped example (`config/labprovider.env.example`) is the schema source of truth and completeness reference. Validation lives in `internal/envfile/schema.go` - one table entry per variable with its validator and the services that require it.
- Docker is the source of truth for what is running; `state.json` is advisory deploy history only.
- There is no CLI path: the legacy bash bootstrap (`bootstrap/`, `templates/`) was the v1 approach and has been removed. `install.sh` and the Go control plane are the only deployment surface.

---

## Scope Discipline

- Keep diffs tightly scoped
- Do not refactor unrelated services
- Extend existing patterns, do not invent new ones
- Maintain consistency across all deployers
- Do not introduce abstraction layers unless explicitly requested
- Do not add migration logic unless explicitly requested
- Prefer explicit, service-scoped changes over generic frameworks

---

## Intended Use and Non-Goals

Built for VCF labs, PoCs, isolated environments, and reproducible demos. This is
an infrastructure services layer, not a full platform, and the constraints below
are deliberate rather than unfinished work:

- Single node only; no orchestration, no clustering, no HA
- Explicit configuration; hidden magic is acceptable only when documented
- Reproducible from scratch, with minimal moving parts
- Usability over production-grade hardening: pragmatic permissions, per-service
  admin credentials in the env file, scoped read-only tokens for the dashboard

---

## Operational Constraints

- Deploys are sequential and dependency-ordered; one deploy runs at a time
- Readiness checks probe externally exposed endpoints - a started container does
  not imply readiness
- Docker is the source of truth for running state; `state.json` is advisory
  history, and it records a per-service config hash so a dependency deployed
  from a stale configuration is redeployed rather than skipped
- The control plane never redeploys itself; upgrade by re-running `install.sh`

---

## Adding or Changing a Service

Every deployer must follow this structure (see any file in `internal/deploy/`):

1. Cross-field validation the schema table cannot express
2. `requireCAReady` when the service consumes a step-ca certificate
3. Directory creation with explicit modes/ownership (`EnsureDir`, `chownR`)
4. Certificate issuance via `IssueCert` (identity-based reuse, full-chain guarantee)
5. Template render (embedded, Go template syntax)
6. `docker compose down` + `up` via the `Compose` runner
7. Readiness verification against the user-facing endpoint (`WaitHTTPSPinned`, `WaitTCP`, DNS probes) - external usability, not internal health endpoints
8. API seeding / token provisioning, idempotent with validity-probe reuse
9. `Remove` with the same data-preservation semantics: runtime files removed, persistent data preserved

Also required:
- Add the service's variables to `internal/envfile/schema.go`
- Add a golden render test for any new template (`UPDATE_GOLDEN=1 go test ./internal/deploy/ -run TestRenderGolden` regenerates)
- Register the deployer in `cmd/control-plane/main.go` in dependency order

---

## Environment Model

- All configuration comes from the managed `labprovider.env`
- Example values (and the completeness reference) in `config/labprovider.env.example`
- No hardcoded environment values in deployers
- Container images are pinned centrally in the env file; never `latest`
- Reject empty values, invalid FQDNs/IPs/CIDRs/ports/paths, and `CHANGE_ME` placeholders

---

## DNS Integration

- Technitium is the only DNS backend; NetBox is the source of truth, reconciled by dns-sync
- Every service has an FQDN in `labprovider.env` and appears in `builtinServiceFQDNs` (netbox.go) so dns-sync publishes it
- `LABPROVIDER_FQDN` is the canonical host identity and the sole reverse PTR target for the host IP
- The canonical host IP object in NetBox is created explicitly from `HOST_IP`, never from record imports; built-in service FQDNs live in its description
- External/custom records come only from the managed `dns.seed`

---

## IP Address Modeling

- `HOST_IP` uses CIDR notation (e.g. `192.168.12.121/24`)
- When CIDR is present, derive and create the surrounding prefix object; plain IPs import as `/32` with no subnet assumptions
- One NetBox IP object per unique address; re-runs never overwrite an existing object's canonical dns_name
- All NetBox seeding must remain idempotent

---

## Filesystem Rules

- Persistent service data under `/opt/labprovider/<service>`; runtime-generated files under `${WORKDIR}` (default `/opt/labprovider/runtime/<service>`)
- Never assume global writable paths; create directories explicitly with correct permissions before use
- Secrets are files with mode 0600 and explicit ownership (uid 1000 for container consumers)

---

## Docker / Compose Rules

- Use `docker compose` via the engine's `Compose` runner
- Explicit image tags (never `latest`), sourced from `labprovider.env`
- Bind mounts for persistence; stacks self-contained per service
- No orchestration layers, no Kubernetes
- Locally built images (chrony, rsyslog, dns-sync) build from embedded or image-baked sources; no registry needed

---

## TLS / Certificate Rules

- step-ca is the internal CA; every HTTPS service uses a step-ca-issued certificate
- Issue via `IssueCert`; certs live under service-specific directories
- Certificate issuance is DNS-independent: every consumer pins `CA_FQDN` to `127.0.0.1` (single-node assumption)
- Postgres data dirs are never nested under a dir that gets a uid-1000 recursive chown

---

## Service Independence

Unless a dependency is explicit in the deployer's `Deps()`, services must remain independently deployable. Cross-service integrations must be additive: token provisioning for a consumer is skipped with a notice when the consumer's directory variable is unset.

---

## What NOT to Do

- No Kubernetes
- No HA / clustering
- No production-grade patterns
- No silent error handling
- No floating versions
- No user-facing application platforms
- No CLI / bash deployment path (the legacy `bootstrap/` was removed; do not reintroduce it)

---

## Testing & Validation

After changes:

1. From the repository root, across both modules:

   ```
   go build github.com/dsjodin/labprovider/services/...
   go vet   github.com/dsjodin/labprovider/services/...
   go test  github.com/dsjodin/labprovider/services/...
   ```

   `go.work` puts `services/control-plane` and `services/dns-sync` in one
   workspace. The module path is deliberate: `./...` from the repository root
   does not cross module boundaries, so it silently skips `services/dns-sync`.
   Also run `gofmt -l services/` (must print nothing) and, when either shell
   script changed, `shellcheck -e SC1091 install.sh reset.sh`. CI
   (`.github/workflows/ci.yml`) runs exactly these.

   For UI changes, dump every page to disk and open them in a browser - the
   templates have no golden tests, so rendering is the check:

   ```
   LABPROVIDER_PREVIEW_DIR=/tmp/preview go test ./internal/server/ -run TestWritePreview
   ```

   Serve the directory over HTTP rather than opening `file://`; the stylesheet
   is linked from an absolute `/static/` path.
2. On a lab host: `sudo bash install.sh`, deploy the affected service from the UI, verify its endpoint
3. Re-deploy to confirm idempotency (cert/token reuse, no data loss)
4. Remove + redeploy to confirm the data-preservation contract

---

## Output Expectations

- Short summary
- Minimal diffs or full files
- No TODOs
- Code must be runnable

---

## Decision Rule

If unsure:

- Choose the simplest solution
- Stay consistent with existing deployers
- Do not change working behavior

Changes should be minimal, follow existing patterns, stay explicit and readable,
avoid unnecessary abstraction and implicit behavior, and prefer external
readiness checks over internal health probes.

---

## Idempotency

- Deploy operations must be idempotent where feasible
- Re-running a deploy must not overwrite or destroy existing state unless explicitly intended
- Existing resources (users, data, certificates, tokens) must be preserved when present and still valid

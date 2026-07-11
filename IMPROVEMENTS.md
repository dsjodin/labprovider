# Improvement Suggestions

Non-documentation issues found during the 2026-07-08 documentation
reconciliation audit. Every entry was verified against the code on main
before inclusion. None of these are implemented by the documentation pass;
each needs its own change.

Format per entry: what, where, why it matters, suggested fix, estimated
blast radius.

---

## 1. Technitium bootstrap uses hardcoded first-boot admin credentials

- What: All bootstrap-phase Technitium API calls authenticate with the
  literal first-boot credentials `admin`/`admin`, and the admin password is
  never rotated.
- Where: `bootstrap/technitium.sh:295` (login for settings calls),
  `bootstrap/technitium.sh:417` (createToken).
- Why it matters: On a re-run after the operator changed the admin password
  (as the module's own comments tell them to), every API step fails. Until
  the operator changes it, the DNS server's admin console is reachable on
  the lab network with default credentials.
- Suggested fix: Add `TECHNITIUM_ADMIN_USER` / `TECHNITIUM_ADMIN_PASSWORD`
  to `provider-box.env`, and have `--technitium` rotate the first-boot
  password to the configured value via `/api/user/changePassword` (verified
  endpoint family in TECHNITIUM_API.md) on first bootstrap, then
  authenticate with the env credentials on re-runs.
- Blast radius: Medium. `technitium.sh` only, plus two env example lines
  and validation; changes re-run behavior on hosts where the password was
  already changed manually.

## 2. --netbox leaks one live superuser API token per run

- What: `netbox_api_auth_header` provisions a fresh superuser token on every
  `--netbox` run for the seeding calls and never deletes it afterwards.
- Where: `bootstrap/netbox.sh:411-439` (provision), no corresponding
  revoke anywhere in `do_netbox`.
- Why it matters: Re-running `--netbox` accumulates unbounded live
  superuser API tokens in NetBox; each one is full-privilege and never
  expires.
- Suggested fix: DELETE the seeding token (its id is in the provision
  response) at the end of `seed_netbox_via_api`, or reuse the dns-sync
  token flow's description tagging so the sweep in suggestion 3 catches it.
- Blast radius: Small. One function in `netbox.sh`; no operator-visible
  behavior change.

## 3. Orphaned-token sweep only matches description-tagged tokens

- What: The pre-provisioning cleanup deletes only tokens whose description
  is exactly "provider-box dns-sync".
- Where: `bootstrap/netbox.sh:479-499` (filter at line 484).
- Why it matters: Tokens created by older code versions (before the
  description was added) or with an edited description are never retired,
  so stale live credentials linger; combined with suggestion 2 the token
  list grows on every deploy.
- Suggested fix: Keep the description filter as the primary match but log a
  count of remaining tokens owned by the superuser, so accumulation is at
  least visible; document manual cleanup. Full name-pattern matching is
  probably over-engineering for the lab scope.
- Blast radius: Small. One function in `netbox.sh`.

## 4. docker_pkgs Docker CE fallback hardcodes the Debian repo

- What: The Docker CE install path always uses
  `https://download.docker.com/linux/debian` with the host's
  `VERSION_CODENAME`.
- Where: `bootstrap/provider-box.sh:192` (gpg key URL),
  `bootstrap/provider-box.sh:198-201` (repo line).
- Why it matters: On Ubuntu the codename (for example `noble`) does not
  exist in the Debian repo, so `apt-get update` 404s and bootstrap fails.
  README documents the Debian assumption, but the code could just handle
  it.
- Suggested fix: Read `ID` from `/etc/os-release` and use
  `linux/${ID}` (docker.com publishes matching `debian` and `ubuntu`
  repos), failing fast on other IDs.
- Blast radius: Small. One function; only affects hosts without Docker
  preinstalled.

## 5. fail messages and checks hardcode compose container names

- What: Error text and a runtime check assume default compose project
  naming (`<dir>-<service>-1`).
- Where: `bootstrap/ca.sh:62`, `bootstrap/ca.sh:73`
  (`docker logs step-ca-step-ca-1`), `bootstrap/dns-sync.sh:175`
  (`docker inspect ... dns-sync-dns-sync-1`, with a compose-based
  fallback).
- Why it matters: If compose changes its naming convention (or a
  `COMPOSE_PROJECT_NAME` is set), the suggested command in the fail message
  is wrong and the inspect fast-path never matches.
- Suggested fix: Use `docker compose -f <file> logs step-ca` phrasing in
  fail messages and rely solely on the `docker compose ps` form in
  `verify_dns_sync_running`.
- Blast radius: Small. Message strings plus one conditional.

## 6. Vestigial TECHNITIUM_FORWARDER reference and unused set-forwarder subcommand

- What: `TECHNITIUM_FORWARDER` was removed from the env model (CHANGELOG
  2026-07-06), but dns-seed still reads it as the default for
  `-forwarders`, and no bootstrap module invokes
  `dns-seed set-forwarder` anymore (the technitium module owns the
  forwarder setting).
- Where: `services/dns-sync/cmd/dns-seed/main.go:175` (env read);
  `runSetForwarder` at `services/dns-sync/cmd/dns-seed/main.go:169-205`.
- Why it matters: Dead configuration surface: an operator setting
  `TECHNITIUM_FORWARDER` expects an effect it no longer has, and the
  subcommand invites a second writer for a setting that has exactly one
  owner by design.
- Suggested fix: Drop the env fallback (keep the explicit flag), or remove
  the `set-forwarder` subcommand entirely.
- Blast radius: Small. dns-seed CLI only; no bootstrap path uses it.

## 7. services/stepca-api absorbed into services/dashboard (RESOLVED)

- What: The design-stage `services/stepca-api` has been folded into the new
  `services/dashboard` as its read-only "Certificates" panel. The reusable
  step-ca BadgerDB reader (`reconcile/badger.go`) was migrated to
  `services/dashboard/internal/certs`; the phase-2 collector parts (SQLite
  inventory, reconcile loop, token-authed HTTP API) were dropped as they are
  explicitly out of v1 scope. The `services/stepca-api/` directory has been
  removed.
- Where: now `services/dashboard/internal/certs/`.
- Status: `STEPCA_STORAGE.md` is retained - the dashboard's cert reader still
  depends on the storage-format details it documents. `step-ca_api_design.md`
  is now historical (it describes the collector API that became phase 2 of the
  dashboard); kept as background, not an active spec.
- Note: `services/dashboard` is now a first-class bootstrap module
  (`bootstrap/dashboard.sh`, flag `--dashboard`, included in `--all` last)
  that publishes `DASHBOARD_FQDN` through `provider_box_builtin_fqdns`. The
  standalone `scripts/run.sh` path is retained for manual use. Remaining
  dashboard phase-2 items (history/collector, UI auth) are tracked in
  `services/dashboard/README.md`.

## 8. --unbound host resolver takeover has no restore path (RESOLVED)

- Resolved by removal: the Unbound backend was deleted entirely
  (see CHANGELOG 2026-07-10). Technitium's marker-based disable/restore
  flow is the only host resolver path. Hosts previously converted by the
  old `configure_resolv_conf` flow must restore `systemd-resolved`
  manually.

## 9. Four copies of the same JSON field extractor, six copies of the CA readiness gate

- What: `json_string_field` (netbox.sh:400),
  `technitium_json_string_field` (technitium.sh:282),
  `sftp_json_string_field` (sftp.sh:204), and
  `authentik_json_string_field` (authentik.sh:228) are the same sed
  one-liner; `require_ca_ready_for_<service>` is duplicated nearly
  verbatim in six modules (authentik.sh:110, depot.sh:66, keycloak.sh:133,
  netbox.sh:183, sftp.sh:72, technitium.sh:55).
- Why it matters: Fixes to one copy (for example the CA reachability check
  or a JSON edge case) silently miss the other five; this is beyond the
  "three similar lines beat an abstraction" threshold.
- Suggested fix: Hoist one `json_string_field` and one
  `require_ca_ready_for <service-label>` helper into
  `bootstrap/provider-box.sh` beside the other shared helpers.
- Blast radius: Medium surface (seven files) but mechanical; no behavior
  change intended.

## 10. Dead fallback ${KEYCLOAK_PORT:-8443} in the NetBox service seed

- What: The service seed block defaults `KEYCLOAK_PORT` to 8443 even
  though `require_netbox_vars` already fails when it is unset.
- Where: `bootstrap/netbox.sh:118` vs the requirement at
  `bootstrap/netbox.sh:135`.
- Why it matters: The fallback can never fire, and it suggests a
  different contract (optional variable) than the validation enforces.
- Suggested fix: Use `${KEYCLOAK_PORT}` plainly.
- Blast radius: Trivial.

## 11. remove_netbox deletes the certificate directory; every other module preserves certs

- What: `--netbox --remove` runs `rm -rf "${NETBOX_DIR}/certs"`, while
  depot/technitium/sftp removals explicitly preserve their cert dirs (and
  validate at deploy time that cert dirs live outside the runtime dir for
  exactly that reason).
- Where: `bootstrap/netbox.sh:762`.
- Why it matters: Redeploying NetBox after a remove always burns a new
  step-ca certificate instead of reusing a valid one, inconsistent with
  the documented "certificates are preserved" convention.
- Suggested fix: Stop deleting `NETBOX_DIR/certs` on remove; the
  identity-aware reuse logic already handles stale certs.
- Blast radius: Small. One line; behavior change only on remove/redeploy
  cycles.

## 12. Technitium settings API secrets travel in the query string

- What: The bootstrap settings calls send the session token and the pfx
  password as URL query parameters
  (`webServiceTlsCertificatePassword=...`).
- Where: `bootstrap/technitium.sh:358-364` (TLS enable), token usage
  throughout the API helpers.
- Why it matters: Query strings can end up in shell history, process
  listings, and proxy/server logs. The Technitium API requires the token
  as a query parameter (verified in TECHNITIUM_API.md), so exposure is
  partly inherent, but the calls run over plain HTTP on 127.0.0.1 during
  bootstrap.
- Suggested fix: Use `curl --data-urlencode` with POST (drop `--get`) so
  parameters move to the request body if the API accepts it (Technitium
  accepts POST form bodies for `/api/settings/set`); verify against the
  pinned image first.
- Blast radius: Small. Local-only exposure today; two functions.

## 13. AGENTS.md is stale (not edited by this pass by instruction)

- What: The agent rules predate Authentik, Technitium, dns-sync, and the
  DNS backend model.
- Where: `AGENTS.md:66-97` ("Existing Services" omits Authentik,
  Technitium, dns-sync; "DNS Integration" requires services to "be
  resolvable via Unbound"); `AGENTS.md:229` (seeding imports only
  `config/unbound.records`).
- Why it matters: Agents follow these rules literally; "resolvable via
  Unbound" contradicts the technitium backend, and the stable-services
  list understates what must not be broken.
- Suggested change: Add Authentik, Technitium, and dns-sync to the stable
  services list; reword DNS Integration to "resolvable via the selected
  DNS backend (generated built-in record list `provider_box_builtin_fqdns`)";
  mention `config/dns.seed` beside `config/unbound.records` in the seeding
  section.
- Blast radius: Documentation only.

## 14. PROJECT_CONTEXT.md is stale (not edited by this pass by instruction)

- What: Core components and the container image list predate Authentik,
  Technitium, and dns-sync.
- Where: `PROJECT_CONTEXT.md:67-97` (components), 140-153 (image list
  missing `AUTHENTIK_IMAGE`, `AUTHENTIK_POSTGRES_IMAGE`,
  `TECHNITIUM_IMAGE`, `DNS_SYNC_IMAGE`), depot table at 113-119 (missing
  the unauthenticated `/products/v1/bundles/lastupdatedtime` alias).
- Suggested change: Add the three services to the component list, extend
  the image list, add the missing depot path row, and describe the
  two-backend DNS model in the Runtime/Service model sections.
- Blast radius: Documentation only.

## 15. env drift check only detects missing variables, not stale ones

- What: `check_provider_env_is_current` flags example variables missing
  from the local env, but a variable removed from the example (like
  `TECHNITIUM_FORWARDER` was) lingers in operator envs forever with no
  signal.
- Where: `bootstrap/provider-box.sh:87-113`.
- Why it matters: Removed variables keep appearing to work (they are
  sourced and exported), masking the fact that nothing consumes them.
- Suggested fix: Emit a non-fatal notice listing local variables that no
  longer exist in the example.
- Blast radius: Small. One function; informational output only.

No dead variables were found in `config/provider-box.env.example`: every
active variable in the example is consumed by at least one module or
template, and every operator-supplied variable required by the modules
exists in the example.

## 16. dashboard cert reader is coupled to step-ca's BadgerDB major version

- RESOLVED (Phase 2, step-ca postgres migration). step-ca now runs on a
  dedicated PostgreSQL backend and the dashboard reads that via
  `github.com/jackc/pgx/v5`. The badger dependency, the `badger/v3` pin, the
  snapshot-copy code, and `badger_fixture_test.go` are removed, so the
  badger-major coupling described below no longer exists. The reader still
  couples to step-ca's stored value SHAPES (DER, and the two JSON blobs), which
  is the residual version-fragility `STEPCA_STORAGE.md` documents - but there is
  no longer an engine/manifest axis. Original entry retained below for history.
- What: `services/dashboard/internal/certs` reads step-ca's embedded
  BadgerDB directly and therefore must import the SAME badger major
  version step-ca writes with. step-ca 0.30.2 (smallstep CLI 0.30.2)
  writes a manifest v7 database, which is the `badger/v3` format, so the
  dashboard pins `github.com/dgraph-io/badger/v3` (v3.2103.5). It was
  originally (mistakenly) on `badger/v4` (manifest v8): a v4 engine
  opening a v7 DB refuses or migrates it, and although the reader only
  ever opens a read-only snapshot copy (so a migration could never touch
  the live DB), on a real lab DB the open would fail and blank the
  Certificates panel.
- Where: `services/dashboard/internal/certs/certs.go` (import + the
  `withSnapshot` open, which keeps `WithReadOnly(true)`),
  `services/dashboard/go.mod`.
- Why it matters: This is the version-fragile coupling `STEPCA_STORAGE.md`
  warns about, now with a second axis (the badger major, not just the
  bucket/key layout). A future step-ca bump that changes its badger major
  requires bumping this import in lockstep - and re-running
  `badger_fixture_test.go`, which writes a v3-format fixture and reads it
  back precisely to catch this drift.
- Suggested fix: When bumping the pinned `CA_IMAGE`, check the step-ca
  release's badger/manifest version and realign this import; keep the
  read-only-open invariant regardless (a matching engine still must not
  be allowed to migrate the snapshot).
- Blast radius: Small and isolated to one package; caught at build time
  (import) and by the fixture test (format).

---

## 17. step-ca on PostgreSQL: decisions and pins (Phase 2)

- What: step-ca was moved from BadgerDB to a DEDICATED postgres backend
  (`stepca-postgres`, `CA_POSTGRES_*`), CRL was enabled, and the dashboard cert
  panel was repointed at postgres through a `SELECT`-only role.
- Pins recorded: `CA_POSTGRES_IMAGE=docker.io/library/postgres:17-alpine`
  (matches `NETBOX_POSTGRES_IMAGE`); dashboard adds
  `github.com/jackc/pgx/v5` and drops `github.com/dgraph-io/badger/v3`.
- Opaque-store fact (load-bearing): on postgres, step-ca still stores opaque
  key-value data - one table per bucket, `nkey`/`nvalue BYTEA`. The tables
  (`x509_certs`, `x509_certs_data`, `revoked_x509_certs`) exist and are
  `SELECT`-able, but cert attributes live inside the `BYTEA` blobs, so the
  reader decodes DER/JSON; SQL cannot filter or join on cert fields. This is
  why the dashboard still owns a decode path rather than issuing relational
  queries.
- stepca-web REJECTED (do not adopt). The Phase 1 assessment evaluated
  `damhau/stepca-web` as the admin console and rejected it: (1) blast radius -
  it demands the postgres creds, the step-ca remote admin API, a cert-issuing
  JWK key, `ca.json` write, and systemd control, so a compromise is a full CA
  compromise; (2) maintenance risk - single-maintainer, low-adoption, no
  release discipline, sitting in the trust-root path; (3) deployment mismatch -
  it assumes a systemd host, while our step-ca is a container. The chosen
  alternative is the existing read-only dashboard panel: no write path, no
  remote admin API, no JWK key - only `SELECT` on the cert tables. If a
  write/revoke UI is ever wanted, that is a separate, scoped decision that would
  require enabling the remote admin API and is re-evaluated then.
- On-host confirmation still needed (a sandbox cannot cover these): the real
  reissue chain against the new root; that the pinned image's pgx honors
  `PGPASSFILE` at runtime; the RO-role grant races nothing (step-ca creates the
  cert tables at startup before `--ca` grants on them); PG TLS posture
  (`sslmode=disable` is loopback-only today); and the CRL endpoint path.
- Blast radius: CA module (`bootstrap/ca.sh`, the CA compose template) plus the
  dashboard package and its compose; a CA rebuild + reissue of all leaves.

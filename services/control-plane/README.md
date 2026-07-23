# labprovider control plane

The control plane is the primary way to run labprovider (v2). It is a single Go
binary in a container that serves the web UI:

- **`/config`** - the configuration wizard (edit/paste/validate/save
  `labprovider.env` and the optional `dns.seed`).
- **`/deploy`** - service selection and deployment with live progress over SSE.
- **`/`** - a **read-only** "current state" dashboard.
- **`/csr`** - paste a PKCS#10 CSR and have step-ca sign it.
- An optional Microsoft-CA web-enrollment emulator (`certsrv`) on a second
  listener, for VCF automated certificate replacement.

`install.sh` at the repo root builds this image from the checkout and runs it
(root, host network, docker socket + `/opt/labprovider` + `/host/etc` mounted);
everything else happens in the browser. See the top-level `README.md` for the
install flow and the service catalog.

The dashboard queries each service's API on request and renders the result.
There is no persistent store, no history, and no writes to any upstream. The
step-ca reader lives in `internal/certs`, reading step-ca's PostgreSQL backend
(the BadgerDB reader was retired when step-ca moved to postgres).

## What the dashboard shows

Five panels. Each degrades independently: if its source is down or not
configured it renders "unavailable" / "not configured" and never blanks the
page or fails the request.

1. **Certificates (step-ca)** - active certs with subject/SANs, provisioner,
   notBefore/notAfter and days-to-expiry, flagged against a warn threshold.
   Read from step-ca's dedicated PostgreSQL backend over `127.0.0.1` with a
   `SELECT`-only role, decoding the opaque cert blobs (see `STEPCA_STORAGE.md`).
2. **DNS (Technitium)** - zone list, managed record count per zone, the
   forwarder in use, and whether the TLS console/API (`:53443`) is reachable.
   Uses the same API shapes as `dns-sync` and the technitium deployer.
3. **IPAM (NetBox)** - prefix and IP-address counts and the `dns_name`
   inventory. Read with a dedicated, minimum-read-scope token.
4. **Services (Docker)** - container name, state, health, uptime and image tag
   for the labprovider stacks, read from the Docker socket (mounted read-only).
5. **Recent errors (logs)** - the last error-level lines per service from a
   bounded log tail. `dns-sync` emits slog JSON, so `level>=error` is parsed;
   non-JSON lines fall back to a token match.

`GET /` renders the HTML page; `GET /api/state` returns the same data as JSON;
`GET /healthz` is a liveness probe.

## Security posture

- **Read-only throughout.** No upstream write path exists in the dashboard code.
  - NetBox: a **dedicated** token with the minimum read scope
    (`ipam.view_prefix`, `ipam.view_ipaddress`; a fine-grained read-only token
    on NetBox versions that support it). Do **not** reuse the `dns-sync` or
    seeding admin token. Auto-provisioned by the netbox deploy.
  - Technitium: a scoped API token (the API has no per-scope tokens, so a
    non-admin user's token is created; it is only ever used for `zones/list`,
    `zones/records/get`, and `settings/get`). Auto-provisioned by the technitium
    deploy.
  - step-ca: the postgres backend is read through a `SELECT`-only role on the
    cert tables; there is no signing path and no write path.
  - Docker socket is mounted `:ro`.
- **Tokens come from files/env, never hardcoded, never logged.** They are read
  from `CONTROL_PLANE_SECRETS_DIR`. Operator-placed (SOPS/age) tokens win over
  auto-provisioning.
- **The control plane serves HTTPS** with a step-ca-issued cert for its FQDN
  once the CA is deployed (restart the container to pick it up). Before that it
  falls back to plaintext HTTP with a logged warning (lab only).
- **No auth on the UI itself (v1).** This is acceptable only on a trusted,
  internal lab network. **TODO before any non-lab use: put the control plane
  behind authentication** (e.g. the Authentik/Keycloak/Zitadel already in this
  repo, or a reverse proxy with auth). Until then, do not expose it beyond the
  lab.

Note the CSR-signing surfaces (`/csr`, `/api/csr/sign`, and the MSCA emulator)
are write paths onto step-ca and are gated only by the emulator's Basic Auth
(`VMSCA_*`) for the certsrv listener; the `/csr` page shares the UI's lack of
auth. Keep the whole control plane on a trusted network.

## The read-only tokens

Both the NetBox and Technitium dashboard panels are auto-provisioned by the
producing deploys (netbox and technitium respectively), so a fresh deploy lights
them up with no manual step. Without a token a panel renders "not configured".
To place tokens yourself (they win while valid), create **dedicated, read-only**
credentials - never reuse the dns-sync or seeding admin tokens - and write them
into `CONTROL_PLANE_SECRETS_DIR` (mode 0600, owner uid 1000):

- `netbox-readonly.token` - a dedicated NetBox read-only token.
- `technitium.token` - a scoped Technitium API token.

**NetBox (`netbox-readonly.token`).** In the NetBox UI as an admin:

1. Create a group (e.g. `dashboard-readonly`) and add an object permission that
   grants only the **view** action on `IPAM > Prefix` and `IPAM > IP address`
   (no add/change/delete). Assign the group that permission.
2. Create a service user (e.g. `dashboard`), add it to that group, and leave it
   without staff/superuser flags.
3. Under that user, create an API token with **Write enabled unchecked**
   (read-only). On NetBox 4.6 the token is the composite `nbt_<key>.<token>` -
   copy the full value.
4. Write it to the secret file:

   ```sh
   install -d -m 0700 "${CONTROL_PLANE_SECRETS_DIR}"
   printf '%s' 'nbt_...' > "${CONTROL_PLANE_SECRETS_DIR}/netbox-readonly.token"
   chmod 0600 "${CONTROL_PLANE_SECRETS_DIR}/netbox-readonly.token"
   chown 1000:1000 "${CONTROL_PLANE_SECRETS_DIR}/netbox-readonly.token"
   ```

The dashboard only ever GETs `ipam/prefixes` and `ipam/ip-addresses`, so a
view-only token on those two models is sufficient.

**Technitium (`technitium.token`).** Technitium's API has no per-scope tokens,
so create a **non-admin** user and use its token (the dashboard only calls
`zones/list`, `zones/records/get`, and `settings/get`):

1. In the Technitium console, add a user (e.g. `dashboard`) that is **not** in
   the `administrators` group.
2. Create a permanent API token for it (console, or
   `/api/user/createToken?user=<u>&pass=<p>&tokenName=dashboard`).
3. Write it to `${CONTROL_PLANE_SECRETS_DIR}/technitium.token` with the same
   `0600` / uid-1000 ownership as above.

## Configuration

All settings are environment variables (see the `CONTROL_PLANE_*` and `VMSCA_*`
blocks in `config/labprovider.env.example` for the documented set). The managed
config is read from `/opt/labprovider/control-plane/labprovider.env`; the binary
also reads the variables directly, so it can run outside Docker for development:

```sh
CONTROL_PLANE_ADDR=:8445 \
CONTROL_PLANE_STEPCA_DSN='postgresql://dashboard_ro@127.0.0.1:5432/stepca?sslmode=disable' \
CONTROL_PLANE_STEPCA_PG_PASSWORD=... \
CONTROL_PLANE_TECHNITIUM_URL=https://dns.sddc.lab:53443 \
CONTROL_PLANE_TECHNITIUM_TOKEN=... \
go run ./cmd/control-plane
```

Without `CONTROL_PLANE_TLS_CERT`/`CONTROL_PLANE_TLS_KEY` it serves plaintext HTTP
with a warning - fine for local development, not for the lab network.

## Phase 2 (explicitly out of scope for v1)

- **History / collector.** v1 fetches on page load only; there is no background
  polling, time series, or store.
- **UI authentication.** Front the control plane with the repo's IdP or a
  reverse-proxy auth layer before any non-lab exposure.

## Development

```sh
go build ./...
go vet ./...
go test ./...
```

Each upstream client has a table-driven parsing test over recorded sample
payloads; the server has tests for per-panel isolation (source up, source down,
not configured). Every deployer template has a golden render test
(`UPDATE_GOLDEN=1 go test ./internal/deploy/ -run TestRenderGolden` regenerates).

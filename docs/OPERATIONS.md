# labprovider - Operations

Configuration, the runtime model, upgrades, backup, and troubleshooting. What
each service *is* lives in [SERVICES.md](SERVICES.md); installing labprovider
is the [README](../README.md).

## Configuration

All configuration is a single flat file, `labprovider.env`, edited in the control plane's `/config` wizard and saved to `/opt/labprovider/control-plane/labprovider.env`. The shipped `config/labprovider.env.example` is the schema source of truth and the completeness reference; a deploy refuses a config missing any variable the example defines.

### Filling it out

Open `/config` and either edit the pre-filled example in place or download it, fill it out locally, and paste it back. The wizard validates on save and reports every problem at once, per variable.

To quickly replace all placeholder passwords with a single value before pasting the file in, you can pre-process a local copy:

```bash
cp config/labprovider.env.example labprovider.env
sed -i "s|CHANGE_ME|VMware1!VMware1!|g" labprovider.env
```

#### Generated secrets

Eleven variables ship empty and are generated on the first deploy rather than
chosen by you. They are read by one container and written by another, so there
is nothing to decide:

`CA_POSTGRES_PASSWORD`, `CA_POSTGRES_RO_PASSWORD`, `NETBOX_POSTGRES_PASSWORD`,
`NETBOX_REDIS_PASSWORD`, `NETBOX_SECRET_KEY`, `AUTHENTIK_SECRET_KEY`,
`AUTHENTIK_PG_PASSWORD`, `ZITADEL_MASTERKEY`, `ZITADEL_PG_PASSWORD`,
`LLDAP_JWT_SECRET`, `LLDAP_KEY_SEED`.

Each generated value is written `0600` to
`/opt/labprovider/control-plane/secrets/<VARIABLE>` and reused on every later
deploy - never rotated behind your back, because several of them are baked into
a PostgreSQL data directory at initdb time. Set a value in `labprovider.env`
instead and yours wins; the variable is only generated while it is empty. Empty
is a valid value for these and only these, so an empty admin password is still a
validation error.

### External DNS records (optional)

To publish external/custom DNS records (VCF nodes, gateways, and other non-labprovider hosts), edit the `dns.seed` block on the same `/config` page. It is imported into NetBox during the netbox and dns-sync deploys, and the reconcile loop then publishes the records via Technitium.

Built-in labprovider service FQDNs are generated automatically from the `*_FQDN` values in `labprovider.env`. You do not add built-in service records to `dns.seed`.

## Configuration Model

`labprovider.env` defines all service configuration. Validation is strict and lives in one schema table (`services/control-plane/internal/envfile/schema.go`): one entry per variable with its validator and the services that require it. The wizard reports every finding at once, per variable, before any deployment changes are made.

Pinned container image versions for Docker-based services are also defined centrally in `labprovider.env`.

For step-ca, no repository-shipped password file is required. labprovider uses `CA_PASSWORD_FILE` when the file exists, materializes `CA_PASSWORD` into a managed `0600` file when set, or generates a random password automatically under `CA_DATA_DIR` when neither input is provided.

### General validation behavior

labprovider rejects:

- empty required values
- invalid FQDNs
- invalid IPs or CIDRs
- invalid absolute-path requirements
- placeholder secret values such as `CHANGE_ME`
- malformed DNS record entries

### Host IP and canonical identity

`HOST_IP` uses IPv4 CIDR notation, for example:

```bash
HOST_IP="192.168.12.121/24"
```

labprovider derives the raw host IPv4 address when services need a plain address and preserves the subnet information when it is useful for NetBox IPAM import.

`LABPROVIDER_FQDN` defines the canonical host identity for the labprovider node.

This distinction is intentional:

- `LABPROVIDER_FQDN` is the canonical host FQDN for the shared labprovider host IP
- service FQDNs such as `DNS_FQDN`, `CA_FQDN`, `DEPOT_FQDN`, `KEYCLOAK_FQDN`, `NETBOX_FQDN`, `S3_FQDN`, `SFTP_FQDN`, `SYSLOG_FQDN`, and `NTP_FQDN` remain service endpoints on the same host

### DNS record format

The `dns.seed` block supports:

```text
<fqdn> <ip>
<fqdn> <ip/cidr>
```

Behavior:

- If a record includes CIDR information, labprovider can derive the surrounding subnet for NetBox
- If a record includes only a plain IP, labprovider imports the host address without guessing the subnet
- Built-in labprovider service records are generated automatically and should not be duplicated in `dns.seed`

### DNS model

The technitium deploy stands up the DNS server; the netbox and dns-sync deploys import `dns.seed` into NetBox (when set) and dns-sync runs a continuous NetBox-to-Technitium reconcile loop. After bring-up, NetBox is the source of truth for lab records; change records in NetBox, not in the seed.

Technitium forwards external queries to `DNS_FORWARDER`. It applies its default recursion policy, which serves RFC1918 (private) client networks; if the lab uses non-RFC1918 ranges, adjust the recursion access control list in the Technitium console so those clients can resolve.

Built-in labprovider service records are generated automatically from the `*_FQDN` values in `labprovider.env`: dns-sync synthesizes them into the desired record set on every reconcile. They are not stored in NetBox, which enforces global IP uniqueness and holds a single canonical host IP object (`LABPROVIDER_FQDN`); that object also remains the reverse PTR target for the host IP.

### Template rendering

Service configuration is rendered from Go text/template files embedded in the control-plane binary, with `missingkey=error`: a reference to an unset variable fails the render rather than silently producing an empty string. Golden render tests pin every template's output.

## Host Assumptions

labprovider assumes:

- Ubuntu or Debian-based host (labprovider is developed and tested on Debian GNU/Linux 13 (trixie), but should work on recent Ubuntu releases)
- root or `sudo` access
- static IP and prefix already configured on the host
- network connectivity from lab consumers to this host
- access to Debian or Ubuntu package repositories (required packages are installed automatically by `install.sh`)
- access to Docker package repositories (required for containerized services)

labprovider uses Docker Compose via `docker compose` (Compose v2). `install.sh` installs Docker idempotently:

- If Docker with Compose v2 already works, existing Docker packages are left untouched and the `docker` service is enabled
- If Docker exists but Compose v2 is missing, only `docker-compose-plugin` is installed
- If Docker is absent, Docker CE is installed from Docker's official apt repository for the detected distribution (`debian` or `ubuntu`); other distributions fail fast with a message to install Docker manually first

## Runtime Model

Everything is containerized. The control plane is a Go binary in a container (root, host network, with the docker socket, `/opt/labprovider`, and `/host/etc` mounted); it execs the bundled docker CLI (compose v2) against the host daemon. Each service is a Docker Compose stack.

| Service   | Runtime |
|-----------|---------|
| Control plane | Docker container (built locally from the checkout by `install.sh`) |
| Chrony   | Docker Compose (image built locally; `cap_add: SYS_TIME` only) |
| rsyslog  | Docker Compose (image built locally) |
| step-ca  | Docker Compose (dedicated `stepca-postgres` backend) |
| Technitium DNS | Docker Compose |
| dns-sync | Docker Compose (image built locally from `services/dns-sync`) |
| VCF offline depot | Docker Compose |
| Keycloak | Docker Compose |
| Authentik | Docker Compose (server, worker, PostgreSQL) |
| Zitadel  | Docker Compose (core, login v2, PostgreSQL, nginx terminator) |
| NetBox   | Docker Compose (NetBox, PostgreSQL, Redis, HTTPS terminator) |
| SeaweedFS (S3) | Docker Compose |
| SFTPGo   | Docker Compose |

chrony, rsyslog, dns-sync, and the control plane have no official upstream image, so their images are built locally by the engine (or by `install.sh`) from embedded or checkout sources; no registry access is needed for them.

## Deploying and Removing Services

Deploy from the `/deploy` page: select services (dependencies are added automatically) and press Deploy. Deploys run sequentially in dependency order, single-flight, with progress streamed live. Docker is the source of truth for what is running; `state.json` is advisory deploy history only.

Removing a service (from the `/deploy` page) stops its containers with `docker compose down` and deletes generated runtime files under `WORKDIR`. Persistent data directories, certificates, and operator secrets are always preserved. The remove path is idempotent and safe to run multiple times.

Removing Technitium additionally restores the stock host resolver configuration: it deletes the labprovider `systemd-resolved` drop-in, points `/etc/resolv.conf` back at the `systemd-resolved` stub, and restarts `systemd-resolved`.

See [Service Reference](#service-reference) for exactly what each service's remove deletes and preserves.

## Upgrading Services

Container image versions are pinned in `labprovider.env` (see [Dependency Updates](#dependency-updates)). To move a containerized service to a new image version, change its `*_IMAGE` pin in `/config`, save, and redeploy that service from `/deploy`; the deploy re-runs its configuration idempotently and the persisted data directory carries state forward.

Before a major-version bump, review the upstream project's release notes for breaking changes to the parts labprovider drives (APIs, settings parameters, data directory format, ports, and the container's user/permissions model), and take a backup of the service's persistent data directory so a rollback is possible.

General upgrade procedure for a containerized service:

```bash
# 1. Back up the persistent data directory (rollback insurance)
sudo tar czf /opt/labprovider/<service>-backup-$(date +%F).tgz -C /opt/labprovider <service>

# 2. Update the pinned image version in /config (edit the relevant *_IMAGE line;
#    never use :latest) and save.

# 3. Remove, then redeploy the single service from /deploy.
```

Rollback: remove the service, restore the pre-upgrade data-directory backup, repin the previous image version, and redeploy.

### Technitium DNS (13.x -> 15.x)

Reviewed release: `docker.io/technitium/dns-server:15.3.0` (upgrade from `13.4.2`, assessed 2026-07-08). The API surface labprovider uses (web service TLS settings, `createToken`, forwarder settings, zone/record CRUD), the data directory layout, ports, and the container uid are unchanged or backward-compatible; the query-string token form still works. A 13.x data directory migrates in place on first start of 15.x (existing zones, records, and API tokens are preserved), so the standard redeploy procedure above applies.

- **Forward-only.** Once 15.x starts on a data directory it rewrites the `*.config` files; a 15.x data directory must NOT be run under 13.x afterward. Rollback to 13.x requires restoring the pre-upgrade backup taken in step 1 - there is no in-place downgrade.
- **DNS stays up across the swap.** The technitium deploy pre-pulls the pinned image before stopping the running container, so when Technitium is the host resolver the image is already cached when DNS briefly goes down during recreate. If the pull fails, the deploy aborts with the old server still running.
- **Behavioral deltas that do not affect labprovider** (documented in `services/dns-sync/TECHNITIUM_API.md`): built-in `internal` reverse zones no longer appear in `zones/list` on 15.x, and deleting a non-existent zone or record now returns an error instead of succeeding silently.

## Dependency Updates

Container image versions are centrally defined in `config/labprovider.env.example` and kept up to date using Renovate in the labprovider repository.

Users consume updated versions by pulling changes to the repository and rebuilding the control-plane image (`install.sh`), then repinning `*_IMAGE` values in `/config` as desired.

## Secrets Inventory

Every secret labprovider generates or persists, where it lives, and what losing or regenerating it means:

| Secret | Path | Owner / mode | Created by | Consequence of loss or regeneration |
|--------|------|--------------|------------|--------------------------------------|
| CA password | `CA_PASSWORD_FILE` (default `CA_DATA_DIR/secrets/password.txt`) | `1000:1000`, `0600` | ca deploy (from `CA_PASSWORD` or generated) | Without it the CA key cannot be decrypted: step-ca stops starting and no certificates can be issued or renewed. It cannot be regenerated; losing it means reinitializing the CA (delete `CA_DATA_DIR` contents) and redeploying every certificate-consuming service, then redistributing the new root certificate. |
| NetBox API token pepper | `NETBOX_DIR/secrets/api_token_pepper` | root, `0600` | netbox deploy (from optional `NETBOX_API_TOKEN_PEPPER` or generated) | Changing or deleting it invalidates every existing NetBox API token, including the dns-sync token. Recover by redeploying netbox (provisions a fresh dns-sync token) and re-issuing any operator tokens. |
| dns-sync NetBox token | `DNS_SYNC_SECRETS_DIR/netbox.token` (composite `nbt_<key>.<token>`) | `1000:1000`, `0600` | netbox deploy (or operator-placed via SOPS/age) | dns-sync stops reconciling (NetBox reads fail). Redeploy netbox to provision a replacement; old tokens with the description "labprovider dns-sync" are retired automatically. |
| dns-sync Technitium token | `DNS_SYNC_SECRETS_DIR/technitium.token` | `1000:1000`, `0600` | technitium deploy (or operator-placed via SOPS/age) | dns-sync stops writing to Technitium. Redeploy technitium to provision a replacement (idempotent; a still-valid stored token is reused). |
| Technitium pfx password | `TECHNITIUM_CERT_DIR/technitium-pfx-password` | `1000:1000`, `0600` | technitium deploy | Needed to rebuild and open `technitium.pfx`. If lost, delete it together with `technitium.pfx` and redeploy technitium; a new password and bundle are generated and re-applied via the settings API. |
| Depot htpasswd | `DEPOT_AUTH_DIR/htpasswd` | root, `0644` | depot deploy (from `DEPOT_BASIC_AUTH_USER`/`_PASSWORD`) | Depot basic auth fails until recreated; regenerated from env on every depot deploy. |
| Zitadel machine PATs | `ZITADEL_DIR/machinekey/{pat.txt,login-client.pat}` | `1000:1000`, `0600` | zitadel first-instance init | Written only during first-instance init on an empty database. If lost while the DB persists, init will not rewrite them; recover by removing the `postgres` and `machinekey` dirs under `ZITADEL_DIR` and redeploying. |

| Generated config secrets | `/opt/labprovider/control-plane/secrets/<VARIABLE>` | root, `0600` | any deploy, for each of the eleven generated variables left empty in `labprovider.env` | Deleting one makes the next deploy generate a replacement, which for the PostgreSQL and Redis passwords will not match the data directory that already exists - the service then fails to authenticate against its own database. Recover by restoring the file, or by removing that service's data directory and redeploying. |

Secrets that live only in `labprovider.env` (admin passwords, the bootstrap client secrets, `VMSCA_PASSWORD`, S3 keys, and so on) are the operator's responsibility; the managed file is stored in plaintext on the host under `/opt/labprovider/control-plane/`.

## Backup and Restore

The irreplaceable state is small and known. Everything else is either
regenerable or service data you already know you have.

| Path | What it is |
|---|---|
| `/opt/labprovider/control-plane/labprovider.env` | the configuration |
| `/opt/labprovider/control-plane/secrets/` | generated secrets, baked into Postgres at initdb time |
| `/opt/labprovider/control-plane/users.json` | operator accounts |
| `/opt/labprovider/control-plane/dns.seed` | external DNS records |
| `/opt/labprovider/step-ca/` | the CA key material |

Lose the CA directory and every certificate the lab issued has to be reissued
and re-trusted. Lose the secrets and the Postgres data directories they were
initialized with cannot be opened.

**From the dashboard:** the Access panel has a *Download backup bundle* link
(`GET /api/backup`), which streams those paths as a gzipped tar with file modes
preserved. `GET /api/backup/contents` lists what is covered without downloading
it.

**From the host**, the same thing:

```bash
tar czf labprovider-backup-$(date +%F).tar.gz \
  /opt/labprovider/control-plane/labprovider.env \
  /opt/labprovider/control-plane/secrets \
  /opt/labprovider/control-plane/users.json \
  /opt/labprovider/control-plane/dns.seed \
  /opt/labprovider/step-ca
```

**To restore**, extract over a fresh install *before* deploying anything: the
generated secrets have to be in place before the first deploy initializes a
Postgres data directory with them.

Service data - Postgres volumes, registry blobs, depot bundles - is
deliberately not in the bundle. It is large, and rsync or a filesystem snapshot
is the right tool for it.

## Troubleshooting

Real failure modes with the messages they produce:

### Port 53 is already in use

```text
Error: Port 53 is already in use and labprovider will not stop the holder automatically.
```

The technitium deploy preflights port 53. `install.sh` disables the `systemd-resolved` stub listener up front; any other holder (a leftover unbound, dnsmasq) must be stopped manually before redeploying. Check `ss -lntup 'sport = :53'`.

### step-ca did not initialize

```text
Error: step-ca did not initialize. Check: docker compose -f <workdir>/step-ca/docker-compose.yml logs step-ca
```

The ca deploy waits for `CA_DATA_DIR/config/ca.json` and then the health endpoint. A partially initialized `CA_DATA_DIR` (for example certs present but no `config/ca.json`, or a password file the container user cannot read) keeps first-start initialization from running. Check the container logs; if the data dir is inconsistent, move aside or delete the contents of `CA_DATA_DIR` and redeploy ca (this reinitializes the CA and invalidates previously issued certificates).

### 403 "Invalid v1 token" / "Invalid v2 token" from NetBox

NetBox 4.6 rejects the legacy `Token <key>` header (`Invalid v1 token`) and rejects Bearer composites whose hash no longer matches (`Invalid v2 token`, typically after the API token pepper changed). Use `Authorization: Bearer nbt_<key>.<token>`, and if the pepper was regenerated, redeploy netbox so a fresh dns-sync token is provisioned.

### HTTP 400 when browsing NetBox by IP

Django only serves hosts listed in `ALLOWED_HOSTS`. `NETBOX_ALLOWED_HOSTS` defaults to the NetBox FQDN only, so browsing by host IP returns a plain `Bad Request (400)`. Browse by FQDN, or add the IP to `NETBOX_ALLOWED_HOSTS` and redeploy netbox.

### Config appears outdated

```text
Error: labprovider.env appears outdated.
Missing variables from the shipped example:
```

After pulling a newer checkout and rebuilding the control-plane image, new variables in the example must be added to your managed config in `/config`; saving is blocked while variables the example defines are missing. A mixed-version symptom of the same root cause is a deploy failing with `Missing required variable: <NAME>`.

### dns-sync reconcile failures

`docker compose -f ${WORKDIR}/dns-sync/docker-compose.yml logs -f` shows structured JSON logs. `status 403` from NetBox means the stored token is no longer valid (see the pepper note above); `invalid-token` from Technitium means `technitium.token` was revoked. Redeploy netbox or technitium to provision replacements.

## Failure Handling

Deployment fails fast if:

- required variables are missing or malformed
- a step-ca certificate cannot be issued
- a service does not become reachable at its user-facing endpoint
- an image build or pull fails

This keeps deployments predictable and reproducible. Readiness checks probe the externally exposed endpoint (a started container does not imply readiness).

## Operational Notes

- Use FQDNs instead of raw IPs where possible
- Ensure both forward and reverse DNS are configured
- Import `keycloak-ca-chain.pem` into VCF when configuring OIDC
- Use `keycloak-ca-roots.pem` only when a roots-only trust bundle is required
- Built-in labprovider service DNS records are generated automatically; reserve `dns.seed` for external and custom records

### DNS behavior warning

Deploying DNS takes over host name resolution: `install.sh` disables the `systemd-resolved` stub listener up front, and the technitium deploy points `/etc/resolv.conf` at Technitium after verifying resolution works; removing Technitium restores the stock configuration.

## Development Safeguards

This repository can optionally be used with local `pre-commit` hooks to catch hygiene issues and prevent accidentally committing secrets.

Install:

```bash
pipx install pre-commit
pre-commit install
```

Run manually:

```bash
pre-commit run --all-files
```

The configured Gitleaks hook scans for secrets before commits are created.

For control-plane development:

```bash
cd services/control-plane
go build ./... && go vet ./... && go test ./...
```

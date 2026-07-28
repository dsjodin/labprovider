# labprovider - Services

What each service is, why it is here, and what it is wired to. Operating them
is [OPERATIONS.md](OPERATIONS.md); installing labprovider is the
[README](../README.md).

## Choosing Services

### Minimum required for VCF bring-up

- DNS: Technitium (with NetBox and dns-sync)
- Chrony for NTP

### Recommended for realistic lab environments

- rsyslog
- SFTPGo for file transfer
- step-ca
- VCF offline depot
- Keycloak

### Optional depending on use case

- SeaweedFS for S3-compatible storage
- Authentik as an alternative identity provider when OIDC plus outbound SCIM 2.0 provisioning is required (for example VCF 9 identity federation)
- Zitadel as an alternative OIDC identity provider, optionally multi-tenant
- The MSCA emulator when VCF should replace certificates automatically via its "Microsoft CA" integration

The `/deploy` page adds dependencies automatically when you select a service. Services otherwise remain independently deployable:

- NetBox does not require Technitium
- S3 and SFTPGo do not require unrelated service configuration
- step-ca is an intentional dependency for services that use labprovider-issued TLS certificates
- dns-sync intentionally depends on Technitium and NetBox; it is the bridge between them

## Service Reference

Services are selected on the `/deploy` page; the engine adds dependencies automatically and deploys in dependency order. "Depends on" lists other labprovider services only; every service also needs a valid `labprovider.env`.

| Service | Purpose | Depends on | Data / runtime dirs | Ports | Secrets it creates | Remove behavior |
|---------|---------|------------|---------------------|-------|--------------------|-----------------|
| chrony | Containerized NTP server | none | `CHRONY_DIR`; runtime under `WORKDIR/chrony` | 123/udp | none | removes runtime dir; preserves `CHRONY_DIR` |
| rsyslog | Containerized central syslog collector | none | `SYSLOG_LOG_DIR`; runtime under `WORKDIR/rsyslog` | `SYSLOG_PORT`/udp+tcp | none | removes runtime dir; preserves `SYSLOG_LOG_DIR` |
| ca | step-ca private CA (dedicated postgres) | none | `CA_DATA_DIR`, `CA_POSTGRES_DATA_DIR`; runtime under `WORKDIR/step-ca` | `CA_PORT`/tcp, `CA_POSTGRES_PORT`/tcp (loopback) | CA password file, `.pgpass`, RO role password | removes runtime dir; preserves data dirs, keys, password |
| technitium | Containerized DNS server | ca | `TECHNITIUM_DATA_DIR`, `TECHNITIUM_CERT_DIR`; runtime under `WORKDIR/technitium` | 53/tcp+udp, `TECHNITIUM_HTTP_PORT`/tcp (loopback), `TECHNITIUM_HTTPS_PORT`/tcp | pfx password, dns-sync + dashboard API tokens | removes runtime dir, restores `systemd-resolved`; preserves data, certs, tokens |
| depot | VCF offline depot (nginx) | ca | `DEPOT_DATA_DIR`, `DEPOT_AUTH_DIR`; runtime under `WORKDIR/depot` | `DEPOT_HTTP_PORT`/tcp (loopback) | `htpasswd` | removes runtime dir and `htpasswd`; preserves data and certs |
| keycloak | Keycloak identity provider | ca | `KEYCLOAK_DIR`; runtime under `WORKDIR/keycloak` | `KEYCLOAK_PORT`/tcp (loopback) | none (credentials from env) | removes runtime dir; preserves `KEYCLOAK_DIR` |
| authentik | Authentik identity provider (OIDC + SCIM) | ca | `AUTHENTIK_DIR`; runtime under `WORKDIR/authentik` | `AUTHENTIK_PORT`/tcp (loopback) | none (credentials from env) | removes runtime dir; preserves `AUTHENTIK_DIR` (certs, data, postgres) |
| zitadel | Zitadel identity provider (OIDC, multi-tenant) | ca | `ZITADEL_DIR`; runtime under `WORKDIR/zitadel` | `ZITADEL_PORT`/tcp | machine-user PATs under `ZITADEL_DIR/machinekey` | removes runtime dir; preserves `ZITADEL_DIR` (certs, postgres, machinekey) |
| netbox | NetBox IPAM/DCIM source of truth | ca | `NETBOX_DIR`, `NETBOX_MEDIA_DIR`, `NETBOX_POSTGRES_DATA_DIR`, `NETBOX_REDIS_DATA_DIR` | `NETBOX_PORT`/tcp (loopback) | API token pepper, dns-sync + dashboard tokens | removes runtime files; preserves media, postgres, redis, certs, and secrets |
| s3 | SeaweedFS S3-compatible storage | none | `S3_DATA_DIR`; runtime under `WORKDIR/s3` | `S3_PORT`/tcp | none (credentials from env) | removes runtime dir; preserves `S3_DATA_DIR` |
| sftp | SFTPGo file transfer | ca | `SFTP_DATA_DIR`, `SFTP_HOME_DIR`; runtime under `WORKDIR/sftpgo` | `SFTP_PORT`/tcp, `SFTP_ADMIN_PORT`/tcp (loopback) | none (credentials from env) | removes runtime dir; preserves data, home, and certs |
| kmip | PyKMIP key server for VCF encryption | ca | `KMIP_DIR`, `KMIP_DATA_DIR`; runtime under `WORKDIR/kmip` | `KMIP_PORT`/tcp | step-ca leaf, client-auth root | removes runtime dir; preserves the key database and certs |
| harbor | Harbor container registry | ca, traefik | `HARBOR_DATA_DIR`; runtime under `WORKDIR/harbor` | `HARBOR_HTTP_PORT`/tcp (loopback) | Harbor's own generated per-component configs and database password | removes runtime dir; preserves images and database |
| dns-sync | NetBox-to-Technitium reconcile loop | ca, technitium, netbox | `DNS_SYNC_DIR`, `DNS_SYNC_SECRETS_DIR`; runtime under `WORKDIR/dns-sync` | none (host networking, outbound only) | none (consumes netbox/technitium tokens) | removes runtime dir; preserves `DNS_SYNC_SECRETS_DIR` |

Notes:

- The control plane itself is installed by `install.sh`, not deployed from `/deploy`; it issues its own leaf certificate after the CA is deployed.
- The firewall is not managed; open the ports in [Required open ports](#required-open-ports) for the services you deploy.

## Service Notes

### Technitium DNS

- Runs the Technitium DNS server via Docker Compose
- Requires step-ca to be initialized first
- Serves DNS on port 53 (TCP and UDP)
- Web console over HTTPS at `https://<DNS_FQDN>` through Traefik; the cleartext console on `TECHNITIUM_HTTP_PORT` (`5380` by default) is published on `127.0.0.1` only
- Web console and API over HTTPS at `https://<DNS_FQDN>:<TECHNITIUM_HTTPS_PORT>` (`53443` by default), using a step-ca-issued certificate
- Persists zone and settings data under `TECHNITIUM_DATA_DIR` and certificates under `TECHNITIUM_CERT_DIR`
- Configures `DNS_FORWARDER` as the upstream forwarder over the settings API and verifies external resolution before pointing the host at itself. The technitium deploy is the only owner of the forwarder setting; dns-sync never touches it.
- Optionally serves DHCP for the lab segment, see below

#### DHCP (optional)

Technitium has a built-in DHCP server that registers its leases in DNS
automatically, so labprovider serves DHCP from the container that is already
running rather than adding a second service. Set `DHCP_ENABLE="true"` and fill
in the scope:

| Variable | Meaning |
|---|---|
| `DHCP_SCOPE_NAME` | Scope name in the Technitium console |
| `DHCP_RANGE_START` / `DHCP_RANGE_END` | The pool handed out |
| `DHCP_SUBNET_MASK` | Dotted mask, e.g. `255.255.255.0` |
| `DHCP_ROUTER` | Default gateway offered to clients |
| `DHCP_LEASE_DAYS` | Lease time in days |

Clients are handed Technitium itself as their DNS server and `SEARCH_DOMAIN` as
their domain; nothing else needs configuring. The deploy rejects a range that
runs backwards or falls outside the `DHCP_ROUTER` subnet, and fails fast if
something else already holds `67/udp`.

**Enabling DHCP moves Technitium onto the host network.** A `DHCPDISCOVER` is a
broadcast to `255.255.255.255:67`, and Docker's published-port proxy does not
deliver it, so a bridged container with `67/udp` published answers nothing. With
`DHCP_ENABLE="true"` the compose file switches to `network_mode: host`, which
has one consequence worth understanding before you turn it on: the cleartext
admin console on `TECHNITIUM_HTTP_PORT` binds the host address instead of
`127.0.0.1` only. Traefik keeps serving `https://<DNS_FQDN>`, routed through its
file provider the same way the control plane is. Leave `DHCP_ENABLE="false"`
unless you need DHCP, and redeploy **both** technitium and traefik after
changing it - the flag is part of each one's configuration, so a run that
includes traefik picks the change up automatically.

Deploy behavior:

- Technitium requires its web TLS certificate as PKCS#12, so the deploy converts the step-ca PEM material into `technitium.pfx` with a generated password persisted at `TECHNITIUM_CERT_DIR/technitium-pfx-password`. The bundle is rebuilt automatically whenever the certificate is reissued.
- An API token for dns-sync is created via the Technitium API and stored at `DNS_SYNC_SECRETS_DIR/technitium.token` (mode `0600`). A stored token is validated and reused while Technitium still accepts it.
- The first-boot `admin`/`admin` credentials are rotated to `TECHNITIUM_ADMIN_PASSWORD` on first deploy and used on re-runs.
- On stock Ubuntu, the `systemd-resolved` stub listener holds `127.0.0.53:53`; `install.sh` disables the stub listener up front. If any other service holds port 53, the deploy fails fast and does not stop it automatically.
- After the DNS listener, forwarder, HTTPS endpoint, and API token are all verified, the deploy points `/etc/resolv.conf` at Technitium (`127.0.0.1`).

Removal behavior:

- Removing Technitium runs `docker compose down`, removes runtime files under `WORKDIR/technitium`, and restores the stock `systemd-resolved` configuration (stub listener re-enabled, `/etc/resolv.conf` pointed back at the stub)
- Persistent data in `TECHNITIUM_DATA_DIR` and certificates in `TECHNITIUM_CERT_DIR` (including the pfx bundle and its password) are preserved

### dns-sync

- Continuously reconciles DNS records from NetBox IPAM into Technitium
- Requires step-ca, Technitium, and NetBox first; both readiness gates pin the lab FQDNs to `127.0.0.1`, so nothing depends on the zone it is about to populate
- The container image (`DNS_SYNC_IMAGE`) is built locally from `services/dns-sync` (baked into the control-plane image); no registry access is needed
- Runs with host networking so its `127.0.0.1` pins reach the host-published NetBox and Technitium ports
- Reconciles every `DNS_SYNC_INTERVAL` (for example `30s`, `5m`, `1h`): one A record per NetBox IP object with a `dns_name`, one PTR per IP (using a deterministically chosen canonical name when several names share an IP), and the built-in service records below
- Built-in labprovider service records are synthesized from the `*_FQDN` values in `labprovider.env` on every reconcile pass. They are deliberately not stored in NetBox (NetBox enforces global IP uniqueness; the host IP is one canonical object with `LABPROVIDER_FQDN` as `dns_name`), and they are A records only so `LABPROVIDER_FQDN` stays the sole PTR target.
- Imports the managed `dns.seed` into NetBox before starting the loop when it is set (idempotent; skipped with a notice otherwise)
- Expects API tokens at `DNS_SYNC_SECRETS_DIR/netbox.token` and `DNS_SYNC_SECRETS_DIR/technitium.token`. Both are auto-provisioned (by the netbox and technitium deploys respectively); placing decrypted tokens there out of band (for example via SOPS/age) is the operator override and wins while the token stays valid.
- After the first reconcile, the deploy verifies over real DNS that `LABPROVIDER_FQDN` and every built-in service FQDN resolve via Technitium
- Logs: `docker compose -f ${WORKDIR}/dns-sync/docker-compose.yml logs -f`

Removal behavior:

- Removing dns-sync runs `docker compose down` and removes runtime files under `WORKDIR/dns-sync`
- Secrets in `DNS_SYNC_SECRETS_DIR` are preserved

### Chrony

- Runs containerized (host networking, `cap_add: SYS_TIME` only); the image is built locally because there is no official chrony container
- Uses configured upstream NTP servers
- Provides NTP service to internal networks
- Persists drift data under `CHRONY_DIR`

### rsyslog

- Runs containerized (host networking); the image is built locally because there is no official rsyslog container
- Exposes centralized syslog via UDP and TCP
- Config is validated (`rsyslogd -N1`) before start
- Intended for log aggregation, not long-term analytics
- Stores logs under `SYSLOG_LOG_DIR`

### step-ca

- Runs as a single-node Smallstep CA via Docker Compose
- Acts as the internal PKI for labprovider services
- Exposed at `https://<CA_FQDN>:<CA_PORT>`
- Persists data under `CA_DATA_DIR` (keys, `ca.json`) and stores CA state in a
  dedicated PostgreSQL backend (`stepca-postgres`)
- Allows service certificates up to `SERVICE_CERT_DURATION` (`8760h` by default)

Behavior:

- Initializes automatically on first start
- Uses `CA_PASSWORD_FILE` as-is when that file already exists
- Materializes `CA_PASSWORD` to a managed `0600` file when provided
- Generates a random CA password automatically when no password input is provided
- Deploying step-ca configures the provisioner default and maximum X.509 certificate duration from `SERVICE_CERT_DURATION`

PostgreSQL backend:

- step-ca stores its state in a DEDICATED postgres container (`stepca-postgres`),
  never shared with NetBox/Authentik/Zitadel (module independence, CA isolation).
- step-ca uses postgres as an opaque key-value store (one table per bucket,
  each `nkey`/`nvalue` `BYTEA`), not a relational schema. Cert attributes live
  inside the `BYTEA` blobs, so anything reading the DB parses the blobs; it
  cannot filter/join on cert fields in SQL.
- The postgres owner password is supplied to step-ca via a mounted `.pgpass`
  file (`PGPASSFILE`), so it never appears in `ca.json`'s `dataSource` DSN.
- The postgres port is published on `127.0.0.1:<CA_POSTGRES_PORT>` only, for the
  host-networked control plane's read-only cert panel. It is never exposed off-host.
- Deploying step-ca also creates a read-only role (`CA_POSTGRES_RO_USER`) with `SELECT` on
  the cert tables only; the dashboard reads through it.
- CRL is enabled (`crl.enabled`) so revocation is served. The remote admin API
  is NOT enabled and there is no write/revoke path in this design.
- On first init the container self-initializes with badger, then the deploy
  rewrites `ca.json`'s `db` stanza to postgresql, restarts, and moves the unused
  badger dir aside (`db.pre-postgres.<timestamp>`, retained, not deleted).
  Switching backends does NOT migrate data: badger state does not move to
  postgres.

Important notes:

- `CA_PASSWORD` is convenient for lab use, but when set in `labprovider.env` it is still stored there in plaintext.
- Reinitialization requires deleting the contents of `CA_DATA_DIR`. The deploy
  refuses to run against an existing badger-backed CA: Phase 2 rebuilds on
  postgres rather than migrating in place.
- `CA_POSTGRES_DATA_DIR` MUST be a sibling of `CA_DATA_DIR`, never nested under
  it (the `chown -R 1000:1000 CA_DATA_DIR` step would corrupt postgres data).
- No repository-shipped static CA password file is required
- The root certificate is available from `/roots.pem`

Rebuild + reissue runbook (run on-host; the deploy does not do the destructive
steps for you):

1. Remove the CA from `/deploy` (stops the stack, preserves data).
2. Wipe the CA state (lab certs are disposable): remove `CA_DATA_DIR` and
   `CA_POSTGRES_DATA_DIR`. Wiping both keeps the new root and the empty postgres
   store consistent; the deploy refuses a new root against a non-empty store.
3. Redeploy step-ca - initializes on postgres, enables CRL, and creates the
   read-only role.
4. Reissue every service certificate against the new root by redeploying each
   certificate-consuming service, one at a time, verifying each before the next.
   The order is: technitium, netbox, authentik, zitadel, depot, sftp, then
   re-run dns-sync (its NetBox/Technitium tokens are reissued too). Keycloak (if
   deployed) reissues the same way. Verify each leaf chains to the new root, e.g.
   `openssl verify -CAfile "$CA_DATA_DIR/certs/root_ca.crt" <service-leaf>.crt`.
5. Confirm CRL is served: `curl --cacert "$CA_DATA_DIR/certs/root_ca.crt"
   --resolve "$CA_FQDN:$CA_PORT:127.0.0.1" https://$CA_FQDN:$CA_PORT/crl`.

Certificate issuance is DNS-independent by design. Every service that requests a certificate (Technitium, depot, Keycloak, Authentik, Zitadel, NetBox, SFTPGo, the control plane) pins `CA_FQDN` to `127.0.0.1` with `--add-host`/`--resolve` instead of resolving it, so certificates can be issued before any DNS backend exists. This relies on the single-node assumption: step-ca and every certificate-consuming service run on the same host, so `127.0.0.1` always reaches the CA. The dns-sync readiness gates and the deploy health checks use the same pinning for the same reason.

### CSR signing and the MSCA emulator

The control plane exposes step-ca's signing to two additional front doors, both going through the same `SignCSR` path (provisioner `admin`, full-chain guarantee) the deployers use:

- **`/csr` page and `POST /api/csr/sign`** - paste a PKCS#10 CSR and get the signed leaf plus chain back.
- **Microsoft-CA web-enrollment emulator** - a `certsrv`-shaped listener so VCF / SDDC Manager can automate certificate replacement using its "Microsoft CA" integration (step-ca offers no such interface natively). Enable it with `VMSCA_ENABLE=true`; it starts as a second listener on `VMSCA_PORT` (default 8446) serving plain HTTP, fronted by Traefik at `https://<VMSCA_FQDN>` (Traefik's wildcard terminates TLS). It serves the endpoints an ADCS web-enrollment client drives (`certfnsh.asp`, `certnew.cer`, `certnew.p7b`, `certcarc.asp`, and the `/certsrv/` credential probe) behind HTTP Basic Auth (`VMSCA_USERNAME`/`VMSCA_PASSWORD`). When enabled, `VMSCA_FQDN` (default `certsrv.sddc.lab`) is published in DNS as an A record to `HOST_IPV4` like every other service name. Point SDDC Manager's Certificate Authority at `https://<VMSCA_FQDN>/certsrv` (no port, through Traefik) with CA Type "Microsoft" and Template Name `VMSCA_TEMPLATE`. See `docs/design/vcf-msca-emulation_design.md` for the full contract, risks, and validation.

### VCF offline depot

- Runs as a single-node nginx container via Docker Compose
- Requires step-ca to be initialized first
- Exposes:
  - HTTPS over `https://<DEPOT_FQDN>` through Traefik, which terminates TLS
  - HTTP on `127.0.0.1:<DEPOT_HTTP_PORT>` as the loopback readiness port
- Stores the managed `htpasswd` file under `DEPOT_AUTH_DIR` (generated with a native APR1-MD5 implementation; no host package needed)
- Persists depot content under `DEPOT_DATA_DIR`
- Creates the expected `PROD/COMP`, `PROD/metadata`, and `PROD/vsan/hcl` directory layout during deploy
- Protects `/PROD/metadata/`, `/PROD/COMP/`, and `/PROD/COMP/Compatibility/VxrailCompatibilityData.json` with basic auth
- Leaves `/PROD/vsan/hcl/`, `/healthz`, `/products/v1/bundles/all`, and `/products/v1/bundles/lastupdatedtime` accessible without authentication
- Renders runtime files under `WORKDIR/depot`

Removal behavior:

- Removing the depot runs `docker compose down`
- Generated runtime files under `WORKDIR/depot` are removed
- The managed `htpasswd` file is removed and recreated on the next deploy
- Persistent depot content under `DEPOT_DATA_DIR` is preserved

#### Filling the depot from a URL

The depot's page in the control plane (`/service/depot`) can pull a bundle
straight into `DEPOT_DATA_DIR`. The control plane does the transfer, not the
browser, which is what makes it usable for VCF bundles:

- Closing the tab does not stop it, and the browser never holds the bytes
- An interrupted transfer resumes from where it stopped on the next attempt,
  using an HTTP `Range` request
- The destination is a path relative to `DEPOT_DATA_DIR` (for example
  `PROD/COMP/bundle.tar`); absolute paths and anything escaping that directory
  are rejected
- Optional basic-auth credentials are used for the transfer and then discarded:
  they are never written to the managed config and never logged
- An optional expected sha256 is verified before the file takes its final name;
  a mismatch leaves the bytes as `<name>.part` and fails the transfer
- Free space is checked before the first byte is written

One transfer runs at a time. It does not go through the deploy engine, so a
long download never blocks a deploy.

### Harbor container registry

Harbor covers the VKS and vSphere image workflows a plain registry cannot:
projects, robot accounts, image-pull secrets, quotas, and replication. It is
the heaviest service labprovider hosts - eight containers, its own PostgreSQL
and Redis - and the only one whose compose file labprovider does not write.

- `goharbor/prepare` generates the compose file and the per-component configs
  from a rendered `harbor.yml`. Hand-writing all of that would mean owning the
  internals of eight containers across every version bump; this is the
  deployment path Harbor supports. The consequence is that the golden test pins
  `harbor.yml` and labprovider's compose override, not the compose file that
  actually runs.
- `HARBOR_VERSION` must match the tag on `HARBOR_PREPARE_IMAGE`: prepare
  validates `harbor.yml`'s `_version` against its own.
- Reached at `https://<HARBOR_FQDN>` through Traefik, which terminates TLS.
  Harbor joins the shared `proxy` network and gets its Traefik labels from a
  `docker-compose.override.yml` that Compose merges with the generated file, so
  nothing edits generated output.
- Harbor's own configuration takes a port number and no bind address, so its
  cleartext port would be published on every interface. The deployer rewrites
  that one line of the generated compose file to bind `127.0.0.1`, and fails
  the deploy if the line is not where it expects - the alternative is exposing
  a cleartext registry login and saying nothing.
- `external_url` is set to the HTTPS name so redirects, `docker login` token
  audiences, and the pull commands in the UI all name the proxy rather than the
  backend port.
- `HARBOR_TRIVY_ENABLE="true"` adds the vulnerability scanner. Off by default:
  it downloads a large database on first start and is not what a VCF lab is
  testing.
- First deploy takes several minutes - Harbor initializes PostgreSQL and runs
  its migrations before the API answers.

**`docker login` and VKS image pulls need the step-ca root in the client's
trust store.** This is the first service where forgetting it is the default
experience: the registry is HTTPS-only through Traefik, and an untrusted issuer
fails as `x509: certificate signed by unknown authority`. Download the root
from the dashboard's Access panel and install it as documented there.

Removal behavior:

- Removing Harbor runs `docker compose down` and deletes `WORKDIR/harbor`
- `HARBOR_DATA_DIR` - the registry blobs and the account database - is preserved

### Keycloak

- Runs via Docker Compose
- Requires step-ca to be initialized first
- Uses a certificate issued by step-ca
- Exposed at `https://<KEYCLOAK_FQDN>` through Traefik; `KEYCLOAK_PORT` (`8443` by default) is the cleartext backend port, published on `127.0.0.1` only
- Seeds an opinionated initial realm from a repository-managed realm import on first deployment

Key files:

- `keycloak.crt` for the Keycloak HTTPS certificate file
- `keycloak.key` for the private key
- `keycloak-ca-chain.pem` for CA chain material
- `keycloak-ca-roots.pem` for roots-only trust use cases
- `keycloak-full-chain.pem` for VCF SSO certificate-chain upload

VCF SSO expects the full IdP TLS chain in leaf, intermediate, root order. Use `keycloak-full-chain.pem` for that upload field.

Realm bootstrap:

- Uses a repository-managed realm derived from a working Keycloak realm export and adapted for labprovider
- Imports one opinionated initial realm, one bootstrap group, and one baseline OIDC client for VCF-style integration
- Bootstraps one initial lab user in the bootstrap realm using `KEYCLOAK_BOOTSTRAP_USERNAME`, `KEYCLOAK_BOOTSTRAP_USER_PASSWORD`, and `KEYCLOAK_BOOTSTRAP_USER_EMAIL_DOMAIN`
- Seeds initial realm state only; it does not provide a generic realm-management framework
- Realm changes are only applied on initial bootstrap; existing realms are not reconciled or modified on subsequent runs

### Authentik

- Runs via Docker Compose with Authentik server, Authentik worker, and PostgreSQL
- Requires step-ca to be initialized first
- Intended for VMware Cloud Foundation 9 identity federation with OIDC authentication and outbound SCIM 2.0 provisioning (which Keycloak lacks)
- Runs in parallel with Keycloak and Zitadel on separate FQDNs and ports when more than one is deployed (including via "Select all"); federate VCF against one of them, using Authentik when SCIM provisioning is required
- Exposed at `https://<AUTHENTIK_FQDN>` through Traefik; `AUTHENTIK_PORT` (`9443` by default) is the cleartext backend port, published on `127.0.0.1` only
- Persists application data under `${AUTHENTIK_DIR}/data` and PostgreSQL data under `${AUTHENTIK_DIR}/postgres`
- Uses a step-ca-issued certificate stored under `${AUTHENTIK_DIR}/certs/<AUTHENTIK_FQDN>` as `fullchain.pem` and `privkey.pem`, picked up by Authentik's built-in certificate discovery
- Bootstraps the `akadmin` password from `AUTHENTIK_ADMIN_PASSWORD` and an API token from `AUTHENTIK_API_TOKEN` on first start
- Seeds an opinionated bootstrap blueprint on startup: one group, one lab user, one OIDC provider (`vcf-oidc`), and one hidden `VCF` application for VCF-style integration
- Sets the default brand web certificate to the discovered step-ca keypair after startup
- OIDC discovery is served at `https://<AUTHENTIK_FQDN>/application/o/vcf/.well-known/openid-configuration`

Blueprint bootstrap:

- Seeds initial state only; existing objects are not overwritten in ways that discard operator changes (the bootstrap user is created once and left alone afterwards)
- Changes to bootstrap client settings in `labprovider.env` are re-applied to the provider on subsequent runs

VCF integration notes:

- Import `${CA_DATA_DIR}/certs/root_ca.crt` into VCF's trusted certificate authorities
- After configuring the VCF Identity Broker, create the SCIM provider in Authentik manually using the SCIM base URL and bearer token that VCF generates, and assign it as the backchannel provider on the `VCF` application. The SCIM URL and token only exist after the VCF side is configured, so this step is not automated.

### Zitadel

- Runs via Docker Compose as four containers: PostgreSQL 17, the Zitadel v4 core server, the `zitadel-login` (Login V2) container, and an nginx TLS terminator that fronts both (v4 dropped CockroachDB support)
- Requires step-ca to be initialized first
- Runs in parallel with Keycloak and Authentik on separate FQDNs and ports when more than one is deployed (including via "Select all")
- Exposed at `https://<ZITADEL_FQDN>:<ZITADEL_PORT>` (`7443` by default), served by the nginx terminator using the step-ca-issued certificate (mounted from `${ZITADEL_DIR}/certs/<ZITADEL_FQDN>`)
- The core runs plain HTTP behind the proxy (`--tlsMode external`, `ExternalSecure=true`); nginx routes `/ui/v2/login` to the login container and everything else to the core
- Persists application state in PostgreSQL 17 under `${ZITADEL_DIR}/postgres`
- `ZITADEL_MASTERKEY` must be EXACTLY 32 characters (Zitadel requirement)
- On first start Zitadel's FirstInstance init creates a human admin (`ZITADEL_ADMIN_USERNAME`/`ZITADEL_ADMIN_PASSWORD`), an admin service account whose PAT is written to `${ZITADEL_DIR}/machinekey/pat.txt`, and the `login-client` service account whose PAT (`${ZITADEL_DIR}/machinekey/login-client.pat`) the Login V2 container authenticates with
- Post-deploy, the control plane uses the admin PAT against the Management API to create a bootstrap project, an OIDC application with `ZITADEL_BOOTSTRAP_CLIENT_REDIRECT_URIS`, a project role (`ZITADEL_BOOTSTRAP_GROUP_NAME`), and a lab user granted that role; the steps tolerate pre-existing objects on re-runs
- Zitadel generates the OIDC client id/secret on creation, so the deploy writes the real issuer/client id/secret to `${ZITADEL_DIR}/certs/<ZITADEL_FQDN>/zitadel-oidc-client.txt` for use with VCF SSO
- **Multi-tenant**: set `ZITADEL_TENANTS` to a comma-separated list of org names to seed each as an isolated organization (its own vcf-sso project, OIDC client, role, and lab user) instead of a single set in the default org. Orgs share the one instance URL (`https://<ZITADEL_FQDN>:<ZITADEL_PORT>`) - the generated org domain (`<name>.<fqdn>`) is a logical identifier for login names and org discovery, not a DNS record or cert. Each tenant's generated client id/secret, issuer, and org login scope (`urn:zitadel:iam:org:id:<orgId>`, which a VCF OIDC request can pass to pin sign-in to that tenant) are written to `zitadel-oidc-<name>.txt`. All tenants currently share the bootstrap client/user template; the default org stays admin-only
- OIDC discovery is served at `https://<ZITADEL_FQDN>:<ZITADEL_PORT>/.well-known/openid-configuration`

### NetBox

- Runs via Docker Compose with NetBox, PostgreSQL, Redis, and a small HTTPS terminator
- Requires step-ca to be initialized first
- Intended as an IPAM, DCIM, and infrastructure source-of-truth service
- Exposed at `https://<NETBOX_FQDN>` through Traefik; `NETBOX_PORT` is the cleartext backend port, published on `127.0.0.1` only
- Persists media under `NETBOX_MEDIA_DIR`
- Persists PostgreSQL data under `NETBOX_POSTGRES_DATA_DIR`
- Persists Redis data under `NETBOX_REDIS_DATA_DIR`
- Uses a step-ca-issued certificate stored under `${NETBOX_DIR}/certs`
- Bootstraps the initial superuser from `NETBOX_SUPERUSER_*` variables on first start
- Seeds labprovider service endpoints into NetBox via the NetBox API after startup
- Imports DNS records from the managed `dns.seed` into NetBox via the API during the netbox deploy when it is set (skipped with a notice otherwise)
- Redeploy NetBox after changing `dns.seed` if you want the changes reflected in NetBox

API tokens (NetBox 4.6):

- NetBox 4.6 hashes API tokens (v2 tokens) and requires a pepper. The deploy generates one (or materializes the optional `NETBOX_API_TOKEN_PEPPER`) and persists it at `NETBOX_DIR/secrets/api_token_pepper`, injecting it into the container as `API_TOKEN_PEPPER_1`. The persisted file is authoritative on re-runs. Do not change or delete it once tokens exist: changing the pepper invalidates every existing API token, including the dns-sync token.
- v2 tokens are used as the composite `nbt_<key>.<token>` with an `Authorization: Bearer` header. The `token` part is only returned at provisioning time. The legacy `Token <key>` header fails against 4.6 with 403 "Invalid v1 token".
- The netbox deploy auto-provisions a dedicated API token for dns-sync (description "labprovider dns-sync") and stores the composite at `DNS_SYNC_SECRETS_DIR/netbox.token` (mode `0600`). A stored, still-valid token is reused, so an operator-placed token (for example decrypted via SOPS/age) wins over auto-provisioning. The per-run superuser seeding token is retired at the end of the deploy.

IPAM behavior:

- `LABPROVIDER_FQDN` is used as the canonical `dns_name` for the shared labprovider host IP object
- Built-in labprovider service FQDNs are stored in that canonical host IP object description
- Built-in service FQDNs remain service endpoints on the same host
- The canonical labprovider host IP object is created explicitly from `HOST_IP` and `LABPROVIDER_FQDN`, not from DNS record imports
- Prefix objects are created when CIDR information is available
- IP address objects use the actual configured mask when CIDR is known, for example `192.168.12.121/24`
- `/32` is used only when subnet information is not available
- One NetBox IP address object is created per unique address value
- Built-in labprovider service FQDNs share the canonical host IP object instead of creating duplicates

This canonical host-IP model is NetBox seeding behavior only. It does not require Technitium to be deployed.

### SeaweedFS S3

- Single-node S3-compatible object storage
- Exposed at `http://<S3_FQDN>:<S3_PORT>` (no TLS by default)
- Data persisted under `S3_DATA_DIR`

Bucket creation example for Velero (deploy the S3 service first):

Install AWS CLI on macOS:

```bash
brew install awscli
```

Install AWS CLI on Debian/Ubuntu:

```bash
sudo apt-get update
sudo apt-get install -y awscli
```

Configure an AWS CLI profile using the S3 credentials from `labprovider.env`:

```bash
aws configure --profile labprovider-s3
```

Use:

```
AWS Access Key ID: <S3 access key>
AWS Secret Access Key: <S3 secret key>
Default region name: us-east-1
Default output format: json
```

Create a `velero-backups` bucket:

```bash
aws --profile labprovider-s3 \
  --endpoint-url http://<S3_FQDN>:<S3_PORT> \
  s3api create-bucket \
  --bucket velero-backups
```

Verify the bucket:

```bash
aws --profile labprovider-s3 \
  --endpoint-url http://<S3_FQDN>:<S3_PORT> \
  s3api list-buckets
```

### SFTPGo

- Single-node SFTP service via Docker Compose
- Requires step-ca to be initialized first for the HTTPS admin UI certificate
- Exposes:
  - SFTP endpoint
  - Client UI over `https://<SFTP_FQDN>/web/client/login`
  - Admin UI over `https://<SFTP_FQDN>/web/admin/login`
  - `SFTP_ADMIN_PORT` is the cleartext backend port, published on `127.0.0.1` only
- Traefik terminates TLS for the admin UI with the wildcard leaf
- Bootstraps the initial admin UI user from `SFTP_ADMIN_USER` and `SFTP_ADMIN_PASSWORD`
- Default admin bootstrap applies only when no SFTPGo admin user already exists
- Optionally creates one backup user when `SFTP_BACKUP_USERNAME`, `SFTP_BACKUP_PASSWORD`, and `SFTP_BACKUP_HOME_DIR` are all set
- Existing backup users are left unchanged on later deploys

The SFTP protocol service remains separate from the HTTPS UI configuration.

### KMIP (PyKMIP)

A KMIP 1.2 key server for the VCF encryption workflows: vSAN encryption, VM
encryption, and the Key Provider configuration in vCenter.

- Runs PyKMIP in a locally built container (no official upstream image), pinned
  by `KMIP_PYKMIP_VERSION`
- Requires step-ca to be initialized first
- Listens on `KMIP_PORT` (5696 by default) with TLS and **client-certificate
  authentication**. This is TTLV over TLS, not HTTP, so Traefik does not front
  it - vCenter connects to the port directly
- Uses a step-ca-issued leaf under `${KMIP_DIR}/certs`, with `HOST_IPV4` as an
  additional SAN so a key provider configured by IP still validates
- Writes `client_ca.crt` (the step-ca root **and** the intermediate) next to the
  leaf, as the anchor set for verifying client certificates. step-ca signs
  leaves with the intermediate, so trusting the root alone would reject a client
  that presents its leaf without the chain
- Stores managed objects in a SQLite database under `KMIP_DATA_DIR`

To wire up vCenter: add a Standard Key Provider pointing at `<KMIP_FQDN>:<KMIP_PORT>`,
upload the step-ca root (`${CA_DATA_DIR}/certs/root_ca.crt`) as the trusted CA,
and give vCenter a client certificate signed by that CA - `/csr` will sign the
CSR vCenter generates.

Removal behavior:

- Stops the container and removes runtime files under `WORKDIR/kmip`
- **Preserves `KMIP_DATA_DIR`**, which holds the keys that decrypt your
  encrypted VMs, and the certificates under `${KMIP_DIR}/certs`

PyKMIP describes itself as a demonstration and testing server. That is exactly
the labprovider use case; do not treat it as a production KMS.

### Dashboard (read-only)

The dashboard is the control plane's `/` page: a **read-only** "current state" view of the labprovider services. It has its own listener and does not alter any other service. See `services/control-plane/README.md` for the full description.

- **What it shows.** Six panels, each fetched on page load under its own short
  timeout and isolated so a dead or unconfigured source renders "unavailable" /
  "not configured" without blanking the page:
  1. Certificates (step-ca) - active certs, subject/SANs, provisioner,
     notBefore/notAfter, days-to-expiry against a warn threshold. Reads step-ca's
     dedicated postgres over `127.0.0.1:<CA_POSTGRES_PORT>` with a `SELECT`-only
     role, decoding the opaque cert blobs (see `docs/design/STEPCA_STORAGE.md`).
  2. DNS (Technitium) - zones, managed record counts, forwarder, TLS reachability.
  3. IPAM (NetBox) - prefix/IP counts and the `dns_name` inventory.
  4. Services - one row per labprovider service (not per container): state,
     address, data directory, last deploy, and the containers backing it as
     detail. State joins the deploy history with the live container list, so a
     service that deployed successfully and then died reads "stopped" rather
     than ready, and a partially-up stack reads "degraded".
  5. Disk - free space on the filesystem holding `WORKDIR`, plus the size of
     each service's data directory. In a lab the depot and SeaweedFS are what
     fill a disk, and the first symptom is otherwise a deploy failing on
     ENOSPC. Capacity is `statfs` on every load; the directory sizes come from
     a walk cached for five minutes and refreshed in the background, so a depot
     holding VCF bundles never slows the page down.
  6. Recent errors - a bounded per-container log tail, parsing `dns-sync`'s slog
     JSON for `level>=error`.
- **Access panel extras.** The LLDAP row expands to the exact values the
  vCenter/VCF SSO wizard asks for when adding an OpenLDAP identity source: the
  LDAP/LDAPS URLs, the user and group base DNs, the bind DN and password, and
  the login/membership attributes. The lldap deploy pre-provisions those
  objects, so the values are derivable - but deriving them by hand is the step
  in VCF integration most likely to be typed wrong.
- **Auto-refresh.** The page re-fetches itself every 30 seconds and repaints,
  with a live/stale badge and a pause toggle in the header. A failed refresh
  shows "stale" rather than leaving values on screen that look current.
- **Per-service restart.** Each running service row has a Restart button
  (`POST /api/services/{name}/restart`), which restarts that service's
  containers. It is not a redeploy: nothing is re-rendered and no certificate is
  reissued, so it is the lever for a wedged container, not for a config change.
  This is the dashboard's only write path, and it sits behind the operator
  login like everything else.
- **Security posture.** Read-only everywhere except the restart button. It uses a
  **dedicated minimum-read-scope NetBox token** (never the dns-sync/bootstrap
  admin token), a scoped Technitium token, and the step-ca DB read-only via a
  `SELECT`-only role. The Docker socket is mounted read-write - it always was,
  since the deploy engine drives `docker compose` through it, and a `:ro`
  bind-mount flag would not restrict the Engine API in any case. The scoped read-only
  tokens are auto-provisioned by the netbox and technitium deploys; operator-placed
  (SOPS/age) tokens win. Tokens come from files/env, never hardcoded or logged.
  The control plane requires an operator login (see the top of this README); the
  dashboard and every API endpoint are behind it.
- **Phase 2 (out of scope).** History/collector (time series), and federating
  the operator login against the repo's own IdP instead of the local account
  store.

## VCF Lab Companion

labprovider provides a lightweight external infrastructure services platform for VMware Cloud Foundation lab and PoC environments.

VCF depends on external services that are not provided by the platform itself.

### Pre-deployment requirements

- DNS for forward and reverse resolution
- NTP for time synchronization

### Post-deployment operational dependencies

- identity provider for OIDC or federation
- centralized logging
- certificate authority (with optional automated cert replacement via the MSCA emulator)
- optional object storage and file transfer services

labprovider packages these services into a single reproducible node so VCF labs can be built without depending on external enterprise infrastructure.

This is especially useful in isolated, homelab, and lab environments where the supporting service plane must be self-contained.

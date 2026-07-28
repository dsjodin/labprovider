# Design notes (history)

These are **pre-implementation** design notes for features that now exist. They
record why each thing was built the way it was, and the alternatives that were
rejected. They are not operator documentation and they are not kept in sync with
the code: where one of them disagrees with `README.md` or with the source, the
code is right and the note is a snapshot of the intent it was built from.

| File | Covers |
|---|---|
| `step-ca_api_design.md` | The step-ca API surface the control plane drives |
| `technitium-dns_design.md` | The DNS model and the NetBox-to-Technitium reconcile contract |
| `traefik-reverse-proxy_design.md` | Single-ingress reverse proxy, wildcard TLS termination |
| `vcf-msca-emulation_design.md` | The `certsrv`-shaped Microsoft-CA emulator for VCF |
| `STEPCA_STORAGE.md` | How step-ca stores certificates in PostgreSQL, which the dashboard's certificate panel decodes |

`STEPCA_STORAGE.md` is the exception worth knowing about: the dashboard reads
those tables directly, so it stays load-bearing rather than purely historical.

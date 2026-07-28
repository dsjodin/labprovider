{{/* Harbor's own configuration file. goharbor/prepare reads this and generates
     the compose file and the per-component configs; see harbor.go for why the
     compose file is generated rather than written here like every other
     service. */}}
hostname: {{.HARBOR_FQDN}}

# Plain HTTP behind Traefik, like every other web service here. The published
# port is rewritten to the loopback after prepare runs; Traefik reaches the
# container over the proxy network, not this port.
http:
  port: {{.HARBOR_HTTP_PORT}}

# What Harbor puts in redirects, in the docker-login token audience, and in the
# pull commands the UI shows. Without this every one of them names the plain
# HTTP port and docker login fails against the proxy.
external_url: https://{{.HARBOR_FQDN}}

harbor_admin_password: {{.HARBOR_ADMIN_PASSWORD}}

database:
  password: {{.HARBOR_DB_PASSWORD}}
  max_idle_conns: 100
  max_open_conns: 900
  conn_max_lifetime: 5m
  conn_max_idle_time: 0

data_volume: {{.HARBOR_DATA_DIR}}

trivy:
  ignore_unfixed: false
  skip_update: false
  offline_scan: false
  security_check: vuln
  insecure: false

jobservice:
  max_job_workers: 10
  job_loggers:
    - STD_OUTPUT
    - FILE
  logger_sweeper_duration: 1

notification:
  webhook_job_max_retry: 3
  webhook_job_http_client_timeout: 3

log:
  level: info
  local:
    rotate_count: 50
    rotate_size: 200M
    location: /var/log/harbor

_version: {{.HARBOR_VERSION}}

proxy:
  http_proxy:
  https_proxy:
  no_proxy:
  components:
    - core
    - jobservice
    - trivy

upload_purging:
  enabled: true
  age: 168h
  interval: 24h
  dryrun: false

cache:
  enabled: false
  expire_hours: 24

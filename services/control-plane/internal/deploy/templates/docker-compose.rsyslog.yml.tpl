services:
  rsyslog:
    image: {{.RSYSLOG_IMAGE}}
    restart: unless-stopped
    # Host networking so syslog serves on the host's SYSLOG_PORT. The log dir
    # is mounted at the same path as on the host because the rendered config's
    # dynafile template contains the host path.
    network_mode: host
    volumes:
      - {{.WORKDIR}}/rsyslog/rsyslog.conf:/etc/rsyslog.conf:ro
      - {{.SYSLOG_LOG_DIR}}:{{.SYSLOG_LOG_DIR}}

services:
  chrony:
    image: {{.CHRONY_IMAGE}}
    restart: unless-stopped
    # Host networking so NTP serves on the host's UDP 123; SYS_TIME is the one
    # capability chronyd needs to discipline the host clock.
    network_mode: host
    cap_drop:
      - ALL
    cap_add:
      - SYS_TIME
    volumes:
      - {{.WORKDIR}}/chrony/chrony.conf:/etc/chrony/chrony.conf:ro
      - {{.CHRONY_DIR}}:/var/lib/chrony

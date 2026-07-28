services:
  mailpit:
    image: {{.MAILPIT_IMAGE}}
    restart: unless-stopped
    environment:
      MP_DATABASE: "/data/mailpit.db"
    # TLS is terminated by Traefik; the web UI serves plain HTTP on 8025 and is
    # fronted at https://{{.MAILPIT_FQDN}}. The UI host port is kept on the
    # loopback for deploy-time readiness only. SMTP is published on all
    # interfaces so lab services can send mail to it.
    ports:
      - "{{.MAILPIT_SMTP_PORT}}:1025"
      - "127.0.0.1:{{.MAILPIT_UI_PORT}}:8025"
    volumes:
      - {{.MAILPIT_DATA_DIR}}:/data
    networks:
      - default
      - proxy
    labels:
      - "traefik.enable=true"
      - "traefik.docker.network=proxy"
      - "traefik.http.routers.mailpit.rule=Host(`{{.MAILPIT_FQDN}}`)"
      - "traefik.http.routers.mailpit.entrypoints=websecure"
      - "traefik.http.routers.mailpit.tls=true"
      - "traefik.http.services.mailpit.loadbalancer.server.port=8025"

networks:
  proxy:
    external: true

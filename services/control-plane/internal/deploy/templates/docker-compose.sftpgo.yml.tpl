services:
  sftpgo:
    image: {{.SFTPGO_IMAGE}}
    restart: unless-stopped
    environment:
      SFTPGO_DATA_PROVIDER__CREATE_DEFAULT_ADMIN: "true"
      SFTPGO_DEFAULT_ADMIN_USERNAME: "{{.SFTP_ADMIN_USER}}"
      SFTPGO_DEFAULT_ADMIN_PASSWORD: "{{.SFTP_ADMIN_PASSWORD}}"
      # TLS is terminated by Traefik; the admin UI serves plain HTTP on 8080
      # and Traefik fronts it at https://{{.SFTP_FQDN}} (proxy trusts the header).
      SFTPGO_HTTPD__BINDINGS__0__PORT: "8080"
      SFTPGO_HTTPD__BINDINGS__0__ENABLE_HTTPS: "0"
      SFTPGO_HTTPD__BINDINGS__0__PROXY_ALLOWED: "0.0.0.0/0"
    ports:
      - "{{.SFTP_PORT}}:2022"
      - "{{.SFTP_ADMIN_PORT}}:8080"
    volumes:
      - {{.SFTP_DATA_DIR}}:/srv/sftpgo
      - {{.SFTP_HOME_DIR}}:/var/lib/sftpgo
    networks:
      - default
      - proxy
    labels:
      - "traefik.enable=true"
      - "traefik.docker.network=proxy"
      - "traefik.http.routers.sftpgo.rule=Host(`{{.SFTP_FQDN}}`)"
      - "traefik.http.routers.sftpgo.entrypoints=websecure"
      - "traefik.http.routers.sftpgo.tls=true"
      - "traefik.http.services.sftpgo.loadbalancer.server.port=8080"

networks:
  proxy:
    external: true

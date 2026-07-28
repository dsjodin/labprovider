services:
  kmip:
    image: {{.KMIP_IMAGE}}
    restart: unless-stopped
    ports:
      # KMIP is TLS with client-certificate auth on its own port, not HTTP, so
      # there is no Traefik router: vCenter connects here directly.
      - "{{.KMIP_PORT}}:5696"
    volumes:
      - {{.WORKDIR}}/kmip/server.conf:/etc/pykmip/server.conf:ro
      - {{.WORKDIR}}/kmip/policies:/etc/pykmip/policies:ro
      - {{.KMIP_DIR}}/certs:/etc/pykmip/certs:ro
      - {{.KMIP_DATA_DIR}}:/var/lib/pykmip
    networks:
      - default

networks:
  default:

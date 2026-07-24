services:
  technitium:
    image: {{.TECHNITIUM_IMAGE}}
    restart: unless-stopped
    environment:
      DNS_SERVER_DOMAIN: "{{.DNS_FQDN}}"
    ports:
      - "53:53/tcp"
      - "53:53/udp"
      - "{{.TECHNITIUM_HTTP_PORT}}:5380/tcp"
      - "{{.TECHNITIUM_HTTPS_PORT}}:53443/tcp"
    volumes:
      - {{.TECHNITIUM_DATA_DIR}}:/etc/dns
      - {{.TECHNITIUM_CERT_DIR}}:/etc/labprovider/technitium-certs:ro
    networks:
      - default
      - proxy
    # Front the plain-HTTP admin console (5380) at https://{{.DNS_FQDN}}. The
    # HTTPS port (53443) stays published for the dashboard/dns-sync consumers.
    labels:
      - "traefik.enable=true"
      - "traefik.docker.network=proxy"
      - "traefik.http.routers.technitium.rule=Host(`{{.DNS_FQDN}}`)"
      - "traefik.http.routers.technitium.entrypoints=websecure"
      - "traefik.http.routers.technitium.tls=true"
      - "traefik.http.services.technitium.loadbalancer.server.port=5380"

networks:
  proxy:
    external: true

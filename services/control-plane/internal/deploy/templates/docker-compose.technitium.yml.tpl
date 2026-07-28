services:
  technitium:
    image: {{.TECHNITIUM_IMAGE}}
    restart: unless-stopped
    environment:
      DNS_SERVER_DOMAIN: "{{.DNS_FQDN}}"
{{- if eq .DHCP_ENABLE "true"}}
    # DHCP requires the host network. A DHCPDISCOVER is a broadcast to
    # 255.255.255.255:67 and docker's published-port proxy does not deliver it,
    # so a bridged container with 67/udp published answers nothing. Here
    # Technitium binds 53, 67, {{.TECHNITIUM_HTTP_PORT}} and 53443 on the host
    # itself, so nothing is published - including the cleartext console, which
    # is no longer loopback-only. Traefik fronts it through the file provider
    # rather than container labels, the same wiring the control plane uses.
    network_mode: host
{{- else}}
    ports:
      - "53:53/tcp"
      - "53:53/udp"
      - "127.0.0.1:{{.TECHNITIUM_HTTP_PORT}}:5380/tcp"
      - "{{.TECHNITIUM_HTTPS_PORT}}:53443/tcp"
{{- end}}
    volumes:
      - {{.TECHNITIUM_DATA_DIR}}:/etc/dns
      - {{.TECHNITIUM_CERT_DIR}}:/etc/labprovider/technitium-certs:ro
{{- if ne .DHCP_ENABLE "true"}}
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
{{- end}}

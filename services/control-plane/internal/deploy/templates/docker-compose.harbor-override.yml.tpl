{{/* Compose loads docker-compose.override.yml automatically alongside the file
     goharbor/prepare generated, which is how Harbor joins the proxy network and
     gets its Traefik labels without labprovider editing generated output.
     "proxy" here is the shared network install.sh creates; "harbor" is the
     network prepare puts every Harbor container on, repeated because listing a
     service's networks replaces the base list rather than adding to it. */}}
services:
  proxy:
    networks:
      - harbor
      - proxy
    labels:
      - "traefik.enable=true"
      - "traefik.docker.network=proxy"
      - "traefik.http.routers.harbor.rule=Host(`{{.HARBOR_FQDN}}`)"
      - "traefik.http.routers.harbor.entrypoints=websecure"
      - "traefik.http.routers.harbor.tls=true"
      - "traefik.http.services.harbor.loadbalancer.server.port=8080"

networks:
  proxy:
    external: true

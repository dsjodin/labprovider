services:
  keycloak:
    image: {{.KEYCLOAK_IMAGE}}
    restart: unless-stopped
    environment:
      KC_BOOTSTRAP_ADMIN_USERNAME: "{{.KEYCLOAK_ADMIN_USER}}"
      KC_BOOTSTRAP_ADMIN_PASSWORD: "{{.KEYCLOAK_ADMIN_PASSWORD}}"
      KC_HEALTH_ENABLED: "true"
      KC_HTTP_MANAGEMENT_HEALTH_ENABLED: "false"
    # TLS is terminated by Traefik; Keycloak serves plain HTTP on 8080, trusts
    # the proxy's X-Forwarded-* headers, and advertises the portless external URL.
    # The host port is kept on the loopback for deploy-time readiness.
    ports:
      - "{{.KEYCLOAK_PORT}}:8080"
    volumes:
      - {{.KEYCLOAK_DIR}}/data:/opt/keycloak/data
      - {{.WORKDIR}}/keycloak/import:/opt/keycloak/data/import:ro
    command:
      - start
      - --import-realm
      - --http-enabled=true
      - --proxy-headers=xforwarded
      - --hostname-strict=false
      - --hostname=https://{{.KEYCLOAK_FQDN}}
    networks:
      - default
      - proxy
    labels:
      - "traefik.enable=true"
      - "traefik.docker.network=proxy"
      - "traefik.http.routers.keycloak.rule=Host(`{{.KEYCLOAK_FQDN}}`)"
      - "traefik.http.routers.keycloak.entrypoints=websecure"
      - "traefik.http.routers.keycloak.tls=true"
      - "traefik.http.services.keycloak.loadbalancer.server.port=8080"

networks:
  proxy:
    external: true

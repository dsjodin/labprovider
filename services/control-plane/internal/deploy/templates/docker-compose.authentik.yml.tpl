services:
  postgresql:
    image: {{.AUTHENTIK_POSTGRES_IMAGE}}
    restart: unless-stopped
    environment:
      POSTGRES_DB: "{{.AUTHENTIK_PG_DB}}"
      POSTGRES_USER: "{{.AUTHENTIK_PG_USER}}"
      POSTGRES_PASSWORD: "{{.AUTHENTIK_PG_PASSWORD}}"
    volumes:
      - {{.AUTHENTIK_DIR}}/postgres:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U {{.AUTHENTIK_PG_USER}} -d {{.AUTHENTIK_PG_DB}}"]
      interval: 15s
      timeout: 5s
      retries: 10

  server:
    image: {{.AUTHENTIK_IMAGE}}
    restart: unless-stopped
    command: server
    shm_size: 512mb
    depends_on:
      postgresql:
        condition: service_healthy
    environment:
      AUTHENTIK_SECRET_KEY: "{{.AUTHENTIK_SECRET_KEY}}"
      AUTHENTIK_POSTGRESQL__HOST: postgresql
      AUTHENTIK_POSTGRESQL__NAME: "{{.AUTHENTIK_PG_DB}}"
      AUTHENTIK_POSTGRESQL__USER: "{{.AUTHENTIK_PG_USER}}"
      AUTHENTIK_POSTGRESQL__PASSWORD: "{{.AUTHENTIK_PG_PASSWORD}}"
      AUTHENTIK_ERROR_REPORTING__ENABLED: "false"
      AUTHENTIK_DISABLE_UPDATE_CHECK: "true"
      AUTHENTIK_BOOTSTRAP_PASSWORD: "{{.AUTHENTIK_ADMIN_PASSWORD}}"
      AUTHENTIK_BOOTSTRAP_TOKEN: "{{.AUTHENTIK_API_TOKEN}}"
    # TLS is terminated by Traefik; the server's plain-HTTP port (9000) is fronted
    # at https://{{.AUTHENTIK_FQDN}} and kept on the loopback for readiness.
    ports:
      - "{{.AUTHENTIK_PORT}}:9000"
    volumes:
      - {{.AUTHENTIK_DIR}}/data:/data
      - {{.WORKDIR}}/authentik/blueprints:/blueprints/custom:ro
    networks:
      - default
      - proxy
    labels:
      - "traefik.enable=true"
      - "traefik.docker.network=proxy"
      - "traefik.http.routers.authentik.rule=Host(`{{.AUTHENTIK_FQDN}}`)"
      - "traefik.http.routers.authentik.entrypoints=websecure"
      - "traefik.http.routers.authentik.tls=true"
      - "traefik.http.services.authentik.loadbalancer.server.port=9000"

  worker:
    image: {{.AUTHENTIK_IMAGE}}
    restart: unless-stopped
    command: worker
    shm_size: 512mb
    depends_on:
      postgresql:
        condition: service_healthy
    environment:
      AUTHENTIK_SECRET_KEY: "{{.AUTHENTIK_SECRET_KEY}}"
      AUTHENTIK_POSTGRESQL__HOST: postgresql
      AUTHENTIK_POSTGRESQL__NAME: "{{.AUTHENTIK_PG_DB}}"
      AUTHENTIK_POSTGRESQL__USER: "{{.AUTHENTIK_PG_USER}}"
      AUTHENTIK_POSTGRESQL__PASSWORD: "{{.AUTHENTIK_PG_PASSWORD}}"
      AUTHENTIK_ERROR_REPORTING__ENABLED: "false"
      AUTHENTIK_DISABLE_UPDATE_CHECK: "true"
      AUTHENTIK_BOOTSTRAP_PASSWORD: "{{.AUTHENTIK_ADMIN_PASSWORD}}"
      AUTHENTIK_BOOTSTRAP_TOKEN: "{{.AUTHENTIK_API_TOKEN}}"
    volumes:
      - {{.AUTHENTIK_DIR}}/data:/data
      - {{.WORKDIR}}/authentik/blueprints:/blueprints/custom:ro

networks:
  proxy:
    external: true

services:
  db:
    image: {{.ZITADEL_POSTGRES_IMAGE}}
    restart: unless-stopped
    environment:
      POSTGRES_DB: "{{.ZITADEL_PG_DB}}"
      POSTGRES_USER: "{{.ZITADEL_PG_USER}}"
      POSTGRES_PASSWORD: "{{.ZITADEL_PG_PASSWORD}}"
    volumes:
      - {{.ZITADEL_DIR}}/postgres:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U {{.ZITADEL_PG_USER}} -d {{.ZITADEL_PG_DB}}"]
      interval: 15s
      timeout: 5s
      retries: 10

  zitadel:
    image: {{.ZITADEL_IMAGE}}
    restart: unless-stopped
    command: start-from-init --masterkeyFromEnv --tlsMode enabled
    depends_on:
      db:
        condition: service_healthy
    environment:
      ZITADEL_MASTERKEY: "{{.ZITADEL_MASTERKEY}}"
      ZITADEL_TLS_ENABLED: "true"
      ZITADEL_TLS_CERTPATH: /certs/zitadel.crt
      ZITADEL_TLS_KEYPATH: /certs/zitadel.key
      ZITADEL_EXTERNALDOMAIN: "{{.ZITADEL_FQDN}}"
      ZITADEL_EXTERNALPORT: "{{.ZITADEL_PORT}}"
      ZITADEL_EXTERNALSECURE: "true"
      ZITADEL_DATABASE_POSTGRES_HOST: db
      ZITADEL_DATABASE_POSTGRES_PORT: "5432"
      ZITADEL_DATABASE_POSTGRES_DATABASE: "{{.ZITADEL_PG_DB}}"
      ZITADEL_DATABASE_POSTGRES_USER_USERNAME: "{{.ZITADEL_PG_USER}}"
      ZITADEL_DATABASE_POSTGRES_USER_PASSWORD: "{{.ZITADEL_PG_PASSWORD}}"
      ZITADEL_DATABASE_POSTGRES_USER_SSL_MODE: disable
      ZITADEL_DATABASE_POSTGRES_ADMIN_USERNAME: "{{.ZITADEL_PG_USER}}"
      ZITADEL_DATABASE_POSTGRES_ADMIN_PASSWORD: "{{.ZITADEL_PG_PASSWORD}}"
      ZITADEL_DATABASE_POSTGRES_ADMIN_SSL_MODE: disable
      ZITADEL_FIRSTINSTANCE_ORG_HUMAN_USERNAME: "{{.ZITADEL_ADMIN_USERNAME}}"
      ZITADEL_FIRSTINSTANCE_ORG_HUMAN_PASSWORD: "{{.ZITADEL_ADMIN_PASSWORD}}"
      ZITADEL_FIRSTINSTANCE_ORG_HUMAN_PASSWORDCHANGEREQUIRED: "false"
      ZITADEL_FIRSTINSTANCE_ORG_MACHINE_MACHINE_USERNAME: labprovider-admin-sa
      ZITADEL_FIRSTINSTANCE_ORG_MACHINE_MACHINE_NAME: labprovider-admin-sa
      ZITADEL_FIRSTINSTANCE_ORG_MACHINE_PAT_EXPIRATIONDATE: "2099-01-01T00:00:00Z"
      ZITADEL_FIRSTINSTANCE_PATPATH: /machinekey/pat.txt
    ports:
      - "{{.ZITADEL_PORT}}:8080"
    volumes:
      - {{.ZITADEL_DIR}}/certs/{{.ZITADEL_FQDN}}:/certs:ro
      - {{.WORKDIR}}/zitadel/machinekey:/machinekey

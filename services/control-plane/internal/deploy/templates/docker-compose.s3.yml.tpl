services:
  seaweedfs-s3:
    image: {{.S3_IMAGE}}
    restart: unless-stopped
    environment:
      AWS_ACCESS_KEY_ID: "{{.S3_ACCESS_KEY}}"
      AWS_SECRET_ACCESS_KEY: "{{.S3_SECRET_KEY}}"
    command:
      - server
      - -dir=/data
      - -s3
      - -ip.bind=0.0.0.0
      - -s3.port={{.S3_PORT}}
    ports:
      - "{{.S3_PORT}}:{{.S3_PORT}}"
    volumes:
      - {{.S3_DATA_DIR}}:/data
    networks:
      - default
      - proxy
    labels:
      - "traefik.enable=true"
      - "traefik.docker.network=proxy"
      - "traefik.http.routers.s3.rule=Host(`{{.S3_FQDN}}`)"
      - "traefik.http.routers.s3.entrypoints=websecure"
      - "traefik.http.routers.s3.tls=true"
      - "traefik.http.services.s3.loadbalancer.server.port={{.S3_PORT}}"

networks:
  proxy:
    external: true

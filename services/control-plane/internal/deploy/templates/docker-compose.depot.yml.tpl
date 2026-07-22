services:
  depot:
    image: {{.DEPOT_IMAGE}}
    restart: unless-stopped
    ports:
      - "{{.DEPOT_HTTP_PORT}}:80"
      - "{{.DEPOT_HTTPS_PORT}}:443"
    volumes:
      - {{.WORKDIR}}/depot/nginx.conf:/etc/nginx/nginx.conf:ro
      - {{.DEPOT_DATA_DIR}}:/usr/share/nginx/html:ro
      - {{.DEPOT_CERT_DIR}}:/etc/labprovider/certs:ro
      - {{.DEPOT_AUTH_DIR}}:/etc/nginx/auth:ro

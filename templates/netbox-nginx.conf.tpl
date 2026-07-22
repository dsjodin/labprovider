server {
  listen 8443 ssl;
  server_name ${NETBOX_FQDN};

  ssl_certificate /etc/labprovider/certs/netbox.crt;
  ssl_certificate_key /etc/labprovider/certs/netbox.key;

  client_max_body_size 25m;

  location / {
    proxy_pass http://netbox:8080;
    proxy_set_header Host ${DOLLAR}host;
    proxy_set_header X-Real-IP ${DOLLAR}remote_addr;
    proxy_set_header X-Forwarded-For ${DOLLAR}proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto https;
  }
}

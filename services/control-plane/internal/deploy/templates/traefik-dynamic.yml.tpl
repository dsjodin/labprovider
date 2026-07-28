tls:
  stores:
    default:
      defaultCertificate:
        certFile: /certs/wildcard.crt
        keyFile: /certs/wildcard.key

http:
  middlewares:
    dashboard-auth:
      basicAuth:
        usersFile: /usersfile

  routers:
    dashboard:
      rule: "Host(`{{.TRAEFIK_FQDN}}`)"
      entryPoints:
        - websecure
      service: api@internal
      middlewares:
        - dashboard-auth
      tls: {}
    control-plane:
      rule: "Host(`{{.CONTROL_PLANE_FQDN}}`)"
      entryPoints:
        - websecure
      service: control-plane
      tls: {}
{{- if eq .VMSCA_ENABLE "true"}}
    certsrv:
      rule: "Host(`{{.VMSCA_FQDN}}`)"
      entryPoints:
        - websecure
      service: certsrv
      tls: {}
{{- end}}
{{- if eq .DHCP_ENABLE "true"}}
    # With DHCP on, Technitium runs on the host network and carries no docker
    # labels for the label provider to discover, so its console is routed here
    # the same way the control plane is.
    technitium:
      rule: "Host(`{{.DNS_FQDN}}`)"
      entryPoints:
        - websecure
      service: technitium
      tls: {}
{{- end}}

  services:
    control-plane:
      loadBalancer:
        servers:
          - url: "http://{{.HOST_IPV4}}:{{.CONTROL_PLANE_PORT}}"
{{- if eq .VMSCA_ENABLE "true"}}
    certsrv:
      loadBalancer:
        servers:
          - url: "http://{{.HOST_IPV4}}:{{.VMSCA_PORT}}"
{{- end}}
{{- if eq .DHCP_ENABLE "true"}}
    technitium:
      loadBalancer:
        servers:
          - url: "http://{{.HOST_IPV4}}:{{.TECHNITIUM_HTTP_PORT}}"
{{- end}}

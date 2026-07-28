services:
  samba:
    image: {{.SAMBA_IMAGE}}
    restart: unless-stopped
    environment:
      SMB_USER: "{{.SAMBA_USER}}"
      SMB_PASSWORD: "{{.SAMBA_PASSWORD}}"
    ports:
      - "{{.SAMBA_PORT}}:445"
    volumes:
      - {{.WORKDIR}}/samba/smb.conf:/etc/samba/smb.conf:ro
      - {{.SAMBA_SHARE_DIR}}:/shares/{{.SAMBA_SHARE_NAME}}

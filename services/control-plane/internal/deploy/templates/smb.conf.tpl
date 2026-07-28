[global]
   workgroup = WORKGROUP
   server string = labprovider
   security = user
   map to guest = never
   server min protocol = SMB2
   load printers = no
   printing = bsd
   printcap name = /dev/null
   disable spoolss = yes

[{{.SAMBA_SHARE_NAME}}]
   path = /shares/{{.SAMBA_SHARE_NAME}}
   valid users = {{.SAMBA_USER}}
   read only = no
   browseable = yes

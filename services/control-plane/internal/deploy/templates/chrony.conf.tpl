server {{.CHRONY_SERVER_1}} iburst
server {{.CHRONY_SERVER_2}} iburst
server {{.CHRONY_SERVER_3}} iburst

allow {{.ALLOW_NET_1}}
allow {{.ALLOW_NET_2}}
allow {{.ALLOW_NET_3}}

driftfile /var/lib/chrony/chrony.drift
makestep 1.0 3
rtcsync

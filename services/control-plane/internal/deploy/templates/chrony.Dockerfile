FROM docker.io/library/alpine:3.22
RUN apk add --no-cache chrony
# chronyd needs its runtime dir for the pidfile and command socket. The OpenRC
# service normally creates it, but we run chronyd directly (as root; see
# chrony.conf's "user root"), so pre-create it root-owned and 0700 - chrony
# rejects a group/other-accessible runtime dir with "Wrong permissions".
RUN mkdir -p /run/chrony && chmod 0700 /run/chrony
CMD ["chronyd", "-d", "-f", "/etc/chrony/chrony.conf"]

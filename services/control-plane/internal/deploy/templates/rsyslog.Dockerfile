FROM docker.io/library/alpine:3.22
RUN apk add --no-cache rsyslog
CMD ["rsyslogd", "-n", "-f", "/etc/rsyslog.conf"]

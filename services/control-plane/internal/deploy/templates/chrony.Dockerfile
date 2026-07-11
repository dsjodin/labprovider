FROM docker.io/library/alpine:3.22
RUN apk add --no-cache chrony
CMD ["chronyd", "-d", "-f", "/etc/chrony/chrony.conf"]

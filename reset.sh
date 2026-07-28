#!/usr/bin/env bash
# Reset a labprovider test host so install.sh can run from a clean slate.
#
# By default this touches only labprovider's own Docker state: the compose
# projects it deploys, the control-plane container, the proxy network, and the
# locally built labprovider/* images. Unrelated containers, images, and volumes
# on the same host are left alone.
#
# It also repoints the host resolver at systemd-resolved, since removing the
# Technitium container while /etc/resolv.conf targets 127.0.0.1 would leave the
# host without DNS.
#
# Usage:
#   sudo bash reset.sh                # asks for confirmation
#   sudo bash reset.sh --yes          # no prompt
#   sudo bash reset.sh --data         # ALSO wipe /opt/labprovider (certs, CA,
#                                     # postgres data, saved config - everything)
#   sudo bash reset.sh --nuke-docker  # ALSO remove every container, image,
#                                     # volume, network, and build cache on the
#                                     # host, labprovider's or not
set -Eeuo pipefail

# The compose projects labprovider creates: one per service workdir, plus the
# NetBox stack, which composes from NETBOX_DIR. A project name is the basename
# of the directory holding the compose file.
LABPROVIDER_PROJECTS=(
  chrony rsyslog step-ca technitium traefik depot keycloak authentik
  zitadel netbox s3 sftpgo mailpit lldap samba kmip harbor dns-sync
)

fail() {
  echo "Error: $*" >&2
  exit 1
}

[[ "$EUID" -eq 0 ]] || fail "Run as root: sudo bash reset.sh"

ASSUME_YES=0
WIPE_DATA=0
NUKE_DOCKER=0
for arg in "$@"; do
  case "$arg" in
    --yes) ASSUME_YES=1 ;;
    --data) WIPE_DATA=1 ;;
    --nuke-docker) NUKE_DOCKER=1 ;;
    *) fail "Unknown flag: $arg (supported: --yes, --data, --nuke-docker)" ;;
  esac
done

if [[ "$NUKE_DOCKER" -eq 1 ]]; then
  echo "This removes ALL Docker containers, images, volumes, networks, and build cache on this host, labprovider's or not."
else
  echo "This removes labprovider's containers, the proxy network, and its locally built images. Other Docker state is left alone."
fi
[[ "$WIPE_DATA" -eq 1 ]] && echo "It will ALSO delete /opt/labprovider (CA keys, postgres data, saved config)."
if [[ "$ASSUME_YES" -ne 1 ]]; then
  read -r -p "Continue? [y/N] " answer
  [[ "$answer" == "y" || "$answer" == "Y" ]] || { echo "Aborted."; exit 0; }
fi

# Repoint the resolver BEFORE stopping containers: once Technitium is gone,
# a resolv.conf targeting 127.0.0.1 leaves the host without DNS.
if grep -qs "127.0.0.1" /etc/resolv.conf; then
  if [[ -f /etc/resolv.conf.labprovider.bak ]]; then
    echo "Restoring /etc/resolv.conf from the backup the technitium deploy took."
    cp /etc/resolv.conf.labprovider.bak /etc/resolv.conf
    rm -f /etc/resolv.conf.labprovider.bak
  elif [[ -e /run/systemd/resolve/resolv.conf ]]; then
    echo "Pointing /etc/resolv.conf back at systemd-resolved."
    rm -f /etc/resolv.conf
    ln -s /run/systemd/resolve/resolv.conf /etc/resolv.conf
    systemctl restart systemd-resolved || true
  else
    # Not every host runs systemd-resolved. Replacing a working resolv.conf
    # with a symlink to a file that does not exist would leave it with no DNS.
    echo "WARNING: /etc/resolv.conf points at Technitium, no backup exists, and this host does not run systemd-resolved."
    echo "         Point /etc/resolv.conf at your resolver manually before re-running install.sh."
  fi
fi

if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  echo "Docker is not running or not installed; skipping Docker cleanup."
elif [[ "$NUKE_DOCKER" -eq 1 ]]; then
  containers="$(docker ps -aq)"
  if [[ -n "$containers" ]]; then
    echo "Stopping and removing all containers."
    # shellcheck disable=SC2086
    docker rm -f $containers >/dev/null
  fi
  echo "Pruning images, volumes, networks, and build cache."
  docker system prune --all --volumes --force >/dev/null
else
  for project in "${LABPROVIDER_PROJECTS[@]}"; do
    containers="$(docker ps -aq --filter "label=com.docker.compose.project=${project}")"
    if [[ -n "$containers" ]]; then
      echo "Removing the ${project} stack."
      # Volumes named by the project go with it; bind-mounted data under
      # /opt/labprovider is untouched unless --data is given.
      # shellcheck disable=SC2086
      docker rm -f -v $containers >/dev/null
    fi
  done

  # install.sh runs the control plane directly, so it carries no compose label.
  docker rm -f labprovider-control-plane >/dev/null 2>&1 || true

  docker network rm proxy >/dev/null 2>&1 || true

  images="$(docker images -q 'labprovider/*')"
  if [[ -n "$images" ]]; then
    echo "Removing locally built labprovider images."
    # shellcheck disable=SC2086
    docker rmi -f $images >/dev/null 2>&1 || true
  fi
  echo "Pruning dangling labprovider layers only; run with --nuke-docker to prune the whole host."
fi

if [[ "$WIPE_DATA" -eq 1 ]]; then
  echo "Deleting /opt/labprovider."
  rm -rf /opt/labprovider
fi

getent hosts deb.debian.org >/dev/null || \
  echo "WARNING: host DNS resolution is not working; check /etc/resolv.conf before re-running install.sh."

echo "Done. Re-run 'sudo bash install.sh' for a fresh deployment."

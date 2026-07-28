#!/usr/bin/env bash
# labprovider installer: the only shell in the v2 model. Installs Docker if
# absent, does the one-time host preparation the containerized control plane
# cannot do itself (systemd-resolved stub listener, systemd-timesyncd), builds
# the control-plane image from this checkout, and starts it. Everything else -
# config, service selection, deployment - happens in the control plane web UI.
set -Eeuo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONTROL_PLANE_IMAGE="${CONTROL_PLANE_IMAGE:-labprovider/control-plane:0.1.0}"
CONTROL_PLANE_NAME="labprovider-control-plane"
CONTROL_PLANE_PORT="${CONTROL_PLANE_PORT:-8445}"
# debug turns on the deploy engine's per-step logging. Set it in the
# environment for one run - CONTROL_PLANE_LOG_LEVEL=debug sudo -E bash
# install.sh - rather than editing this file.
CONTROL_PLANE_LOG_LEVEL="${CONTROL_PLANE_LOG_LEVEL:-info}"

fail() {
  echo "Error: $*" >&2
  exit 1
}

[[ "$EUID" -eq 0 ]] || fail "Run as root: sudo bash install.sh"

# --- Docker (port of the bash docker_pkgs, with the Ubuntu repo fix) --------
install_docker() {
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    echo "Docker with Compose v2 already installed."
    systemctl enable --now docker
    return 0
  fi
  if command -v docker >/dev/null 2>&1; then
    echo "Docker is installed but Compose v2 is missing; installing docker-compose-plugin."
    apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get install -y docker-compose-plugin
    systemctl enable --now docker
    docker compose version >/dev/null 2>&1 || fail "docker compose v2 is required but not available."
    return 0
  fi

  echo "Installing Docker CE from Docker's official apt repository."
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl

  local os_id codename
  os_id="$(. /etc/os-release && echo "${ID}")"
  codename="$(. /etc/os-release && echo "${VERSION_CODENAME}")"
  [[ "${os_id}" == "debian" || "${os_id}" == "ubuntu" ]] || \
    fail "Unsupported distribution '${os_id}'; install Docker with Compose v2 manually and re-run."

  install -m 0755 -d /etc/apt/keyrings
  if [[ ! -f /etc/apt/keyrings/docker.asc ]]; then
    curl -fsSL "https://download.docker.com/linux/${os_id}/gpg" -o /etc/apt/keyrings/docker.asc
    chmod a+r /etc/apt/keyrings/docker.asc
  fi
  cat > /etc/apt/sources.list.d/docker.list <<EOF
deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/${os_id} ${codename} stable
EOF
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y \
    docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  systemctl enable --now docker
  docker compose version >/dev/null 2>&1 || fail "docker compose v2 is required but not available."
}

# --- One-time host prep ------------------------------------------------------
# Technitium always owns port 53 in the v2 model, so the systemd-resolved stub
# listener is disabled up front (keeping host resolution working through the
# transition); chrony is containerized, so systemd-timesyncd is disabled.
prepare_host() {
  if systemctl is-enabled systemd-resolved >/dev/null 2>&1; then
    echo "Disabling the systemd-resolved DNS stub listener (Technitium will own port 53)."
    install -d -m 0755 /etc/systemd/resolved.conf.d
    cat > /etc/systemd/resolved.conf.d/labprovider.conf <<CONF
# Managed by labprovider (install.sh). Remove and restart systemd-resolved to undo.
[Resolve]
DNSStubListener=no
CONF
    if [[ -L /etc/resolv.conf && "$(readlink /etc/resolv.conf)" == *stub-resolv.conf ]]; then
      ln -sf /run/systemd/resolve/resolv.conf /etc/resolv.conf
    fi
    systemctl restart systemd-resolved
    getent hosts deb.debian.org >/dev/null || \
      fail "Host DNS resolution broke after disabling the resolved stub listener; fix /etc/resolv.conf and re-run."
  fi

  if systemctl is-enabled systemd-timesyncd >/dev/null 2>&1; then
    echo "Disabling systemd-timesyncd (chrony runs containerized)."
    systemctl disable --now systemd-timesyncd || true
  fi

  install -d -m 0755 /opt/labprovider /opt/labprovider/control-plane

  # Shared network the Traefik ingress and the bridge service stacks join. It
  # must exist before any stack references it as an external network, so it is
  # created here rather than by the Traefik deploy module.
  docker network inspect proxy >/dev/null 2>&1 || docker network create proxy
}

# --- Build and run the control plane -----------------------------------------
run_control_plane() {
  # Stamp the commit into the binary so the running build is identifiable from
  # /healthz and the sidebar. --dirty marks a build from an edited tree, which
  # is the one you most need to recognize later. A checkout with no git (a
  # tarball) reports "dev" rather than failing the install.
  local version
  version="$(git -C "${REPO_ROOT}" describe --always --dirty --tags 2>/dev/null || echo dev)"
  echo "Building ${CONTROL_PLANE_IMAGE} (${version}) from ${REPO_ROOT}."
  docker build -t "${CONTROL_PLANE_IMAGE}" --build-arg "VERSION=${version}" \
    -f "${REPO_ROOT}/services/control-plane/Dockerfile" "${REPO_ROOT}"

  docker rm -f "${CONTROL_PLANE_NAME}" >/dev/null 2>&1 || true
  echo "Starting the control plane."
  # The control plane serves plain HTTP; Traefik terminates TLS and fronts it at
  # https://<CONTROL_PLANE_FQDN> once the ingress is deployed. Before that (first
  # config/deploy), reach it directly at http://<host>:${CONTROL_PLANE_PORT}.
  docker run -d --name "${CONTROL_PLANE_NAME}" \
    --restart unless-stopped \
    --network host \
    -e CONTROL_PLANE_ADDR=":${CONTROL_PLANE_PORT}" \
    -e CONTROL_PLANE_LOG_LEVEL="${CONTROL_PLANE_LOG_LEVEL}" \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -v /opt/labprovider:/opt/labprovider \
    -v /etc:/host/etc \
    "${CONTROL_PLANE_IMAGE}" >/dev/null
}

# --- Verify it actually came up ----------------------------------------------
# A bad mount, an already-bound port, or a startup error leaves the container
# exited; without this the operator is told it is running and finds out when the
# browser times out.
wait_control_plane() {
  local url="http://127.0.0.1:${CONTROL_PLANE_PORT}/healthz"
  local probe
  if command -v curl >/dev/null 2>&1; then
    probe=(curl -fsS --max-time 2 "$url")
  elif command -v wget >/dev/null 2>&1; then
    probe=(wget -q -T 2 -O /dev/null "$url")
  else
    echo "Neither curl nor wget is available; skipping the startup check."
    echo "Verify with: docker logs ${CONTROL_PLANE_NAME}"
    return 0
  fi
  for _ in $(seq 1 30); do
    if "${probe[@]}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "Error: the control plane did not answer ${url} within 30s." >&2
  echo "Recent container logs:" >&2
  docker logs --tail 50 "${CONTROL_PLANE_NAME}" >&2 2>&1 || true
  exit 1
}

install_docker
prepare_host
run_control_plane
wait_control_plane

host_ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
echo
echo "labprovider control plane is running."
echo "Open http://${host_ip:-<host-ip>}:${CONTROL_PLANE_PORT}/setup to create the operator account,"
echo "then /config to upload your configuration and /deploy to deploy services."

# Until an operator account exists, /setup is reachable by anyone on the
# segment - and it creates a root-equivalent account. The token makes that an
# authenticated bootstrap; it is spent by the first account and deleted.
setup_token="$(cat /opt/labprovider/control-plane/setup-token 2>/dev/null || true)"
if [[ -n "${setup_token}" ]]; then
  echo
  echo "Setup token (required on /setup, single use):"
  echo "  ${setup_token}"
fi
echo "Until step-ca issues the control plane its own certificate this is plain HTTP,"
echo "so that first password crosses the network in the clear; use a trusted lab network."

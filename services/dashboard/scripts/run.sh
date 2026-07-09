#!/usr/bin/env bash
# One correct way to start the standalone dashboard. Runs the documented compose
# command with the shared env file and resolves DASHBOARD_DOCKER_GID from the
# host docker group so the read-only socket mount is usable by uid 1000.
#
# Usage: services/dashboard/scripts/run.sh [ENV_FILE] [-- extra compose args]
# ENV_FILE defaults to config/provider-box.env at the repo root.
# Example: services/dashboard/scripts/run.sh            # up -d --build
#          services/dashboard/scripts/run.sh -- down    # stop it
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVICE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
REPO_ROOT="$(cd "${SERVICE_DIR}/../.." && pwd)"

fail() {
  echo "Error: $*" >&2
  exit 1
}

ENV_FILE="${REPO_ROOT}/config/provider-box.env"
if [[ "${1:-}" != "" && "${1:-}" != "--" ]]; then
  ENV_FILE="$1"
  shift
fi
[[ "${1:-}" == "--" ]] && shift

[[ -f "${ENV_FILE}" ]] || fail "Missing env file ${ENV_FILE}"
command -v docker >/dev/null || fail "docker is required"
docker compose version >/dev/null 2>&1 || fail "docker compose v2 is required"

# Resolve the host docker gid; a shell export overrides the env-file default so
# the socket mount works without hand-editing config.
if docker_gid="$(getent group docker | cut -d: -f3)" && [[ -n "${docker_gid}" ]]; then
  export DASHBOARD_DOCKER_GID="${docker_gid}"
else
  echo "Warning: no 'docker' group found; leaving DASHBOARD_DOCKER_GID from ${ENV_FILE}." >&2
fi

# Default action is up -d --build; pass anything after -- to override.
if [[ $# -eq 0 ]]; then
  set -- up -d --build
fi

cd "${SERVICE_DIR}"
exec docker compose --env-file "${ENV_FILE}" "$@"

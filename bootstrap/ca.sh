#!/usr/bin/env bash

require_ca_vars() {
  local var
  for var in WORKDIR CA_FQDN CA_PORT CA_DATA_DIR CA_NAME CA_PROVISIONER_NAME SERVICE_CERT_DURATION CA_ENABLE_ACME CA_IMAGE \
             CA_POSTGRES_IMAGE CA_POSTGRES_DB CA_POSTGRES_USER CA_POSTGRES_PASSWORD CA_POSTGRES_PORT CA_POSTGRES_DATA_DIR \
             CA_POSTGRES_RO_USER CA_POSTGRES_RO_PASSWORD; do
    [[ -n "${!var:-}" ]] || fail "Missing required variable: $var"
  done

  validate_var_path "${WORKDIR}"
  validate_var_fqdn "${CA_FQDN}"
  validate_var_port "${CA_PORT}"
  validate_var_path "${CA_DATA_DIR}"
  validate_service_cert_duration "${SERVICE_CERT_DURATION}"
  [[ "${CA_IMAGE}" == *:* ]] || fail "CA_IMAGE must include an explicit image tag"
  [[ "${CA_IMAGE}" != *:latest ]] || fail "CA_IMAGE must not use the latest tag"
  resolve_ca_password_file
  validate_var_path "${CA_PASSWORD_FILE}"
  [[ "${CA_ENABLE_ACME}" == "true" || "${CA_ENABLE_ACME}" == "false" ]] || \
    fail "CA_ENABLE_ACME must be either true or false"
  [[ "${CA_PASSWORD_FILE}" == "${CA_DATA_DIR}"/* ]] || \
    fail "CA_PASSWORD_FILE must be located under CA_DATA_DIR so it is mounted into the container"
  if [[ -n "${CA_PASSWORD:-}" ]]; then
    validate_ca_password_value "${CA_PASSWORD}"
  fi

  # Dedicated postgres backend (module independence: NOT shared with any other
  # service). CA_POSTGRES_DATA_DIR is a sibling of CA_DATA_DIR, never nested:
  # do_ca runs `chown -R 1000:1000 CA_DATA_DIR` every run, which would corrupt
  # postgres data (uid 70) on redeploy if it lived under CA_DATA_DIR.
  [[ "${CA_POSTGRES_IMAGE}" == *:* ]] || fail "CA_POSTGRES_IMAGE must include an explicit image tag"
  [[ "${CA_POSTGRES_IMAGE}" != *:latest ]] || fail "CA_POSTGRES_IMAGE must not use the latest tag"
  validate_pg_identifier CA_POSTGRES_DB "${CA_POSTGRES_DB}"
  validate_pg_identifier CA_POSTGRES_USER "${CA_POSTGRES_USER}"
  validate_pg_identifier CA_POSTGRES_RO_USER "${CA_POSTGRES_RO_USER}"
  [[ "${CA_POSTGRES_RO_USER}" != "${CA_POSTGRES_USER}" ]] || \
    fail "CA_POSTGRES_RO_USER must differ from CA_POSTGRES_USER (the read-only role must not be the owner)"
  validate_var_port "${CA_POSTGRES_PORT}"
  validate_var_path "${CA_POSTGRES_DATA_DIR}"
  [[ "${CA_POSTGRES_DATA_DIR}" != "${CA_DATA_DIR}"/* ]] || \
    fail "CA_POSTGRES_DATA_DIR must NOT be nested under CA_DATA_DIR (the CA_DATA_DIR chown would corrupt postgres data). Use a sibling path such as /opt/provider-box/stepca-postgres."
  validate_ca_password_value "${CA_POSTGRES_PASSWORD}"
  validate_ca_password_value "${CA_POSTGRES_RO_PASSWORD}"
  resolve_ca_pgpassfile
  validate_var_path "${CA_PGPASSFILE}"
  [[ "${CA_PGPASSFILE}" == "${CA_DATA_DIR}"/* ]] || \
    fail "CA_PGPASSFILE must be located under CA_DATA_DIR so it is mounted into the container"
}

default_ca_pgpassfile() {
  printf '%s/secrets/pgpass' "${CA_DATA_DIR}"
}

resolve_ca_pgpassfile() {
  if [[ -z "${CA_PGPASSFILE:-}" ]]; then
    CA_PGPASSFILE="$(default_ca_pgpassfile)"
  fi
  export CA_PGPASSFILE
}

require_ca_remove_vars() {
  local var
  for var in WORKDIR CA_DATA_DIR; do
    [[ -n "${!var:-}" ]] || fail "Missing required variable: $var"
  done

  validate_var_path "${WORKDIR}"
  validate_var_path "${CA_DATA_DIR}"
}

# Guard against a prior `docker compose` run with an empty CA_DATA_DIR (an
# unset variable) having created ${CA_DATA_DIR}/certs/root_ca.crt as a
# DIRECTORY: Docker auto-creates a missing bind-mount source as a directory, so
# a blank path turned this trust-root file into a directory, broke step-ca init
# ("file is a directory") and destroyed the running CA. Refuse to proceed on a
# corrupted root so the operator notices instead of looping on a broken init.
require_ca_root_not_corrupted() {
  local root="${CA_DATA_DIR}/certs/root_ca.crt"
  if [[ -d "${root}" ]]; then
    fail "step-ca root certificate ${root} is a DIRECTORY, not a file. This is almost always a prior 'docker compose' run with an empty CA_DATA_DIR (an unset variable) auto-creating the missing bind-mount source as a directory - it can destroy the CA. Remove it (rm -rf '${root}') and restore or re-initialize the CA before re-running --ca."
  fi
  if [[ -e "${root}" && ! -f "${root}" ]]; then
    fail "step-ca root certificate ${root} exists but is not a regular file (likely a bad bind mount from an empty CA_DATA_DIR). Investigate and restore a valid root_ca.crt before re-running --ca."
  fi
}

normalize_ca_password_files() {
  local file

  for file in \
    "${CA_PASSWORD_FILE}" \
    "${CA_DATA_DIR}/secrets/password"
  do
    [[ -f "${file}" ]] || continue
    chown 1000:1000 "${file}"
    chmod 0600 "${file}"
  done
}

# step ca init runs asynchronously at first container start; key generation
# takes seconds. Wait for the configuration to appear, then for the CA to
# answer on its health endpoint, before doing anything that depends on it.
wait_for_ca_init() {
  local ca_config="${CA_DATA_DIR}/config/ca.json"
  local attempt

  for attempt in $(seq 1 30); do
    [[ -f "${ca_config}" ]] && break
    sleep 2
  done
  [[ -f "${ca_config}" ]] || \
    fail "step-ca did not initialize. Check: docker logs step-ca-step-ca-1"

  for attempt in $(seq 1 30); do
    if curl --silent --show-error --fail \
      --cacert "${CA_DATA_DIR}/certs/root_ca.crt" \
      --resolve "${CA_FQDN}:${CA_PORT}:127.0.0.1" \
      "https://${CA_FQDN}:${CA_PORT}/health" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  fail "step-ca did not initialize. Check: docker logs step-ca-step-ca-1"
}

configure_ca_service_cert_duration() {
  local ca_config="${CA_DATA_DIR}/config/ca.json"

  echo "Configuring step-ca service certificate duration: ${SERVICE_CERT_DURATION}"
  docker run --rm \
    --user 1000:1000 \
    -v "${CA_DATA_DIR}:/home/step" \
    "${CA_IMAGE}" \
    step ca provisioner update "${CA_PROVISIONER_NAME}" \
      --x509-default-dur="${SERVICE_CERT_DURATION}" \
      --x509-max-dur="${SERVICE_CERT_DURATION}" \
      --ca-config /home/step/config/ca.json || \
    fail "Failed to configure step-ca provisioner certificate duration."
  chown 1000:1000 "${ca_config}"
  chmod 0600 "${ca_config}"
}

# Write the libpq .pgpass file step-ca's pgx driver reads (via PGPASSFILE) so
# the postgres password stays out of the dataSource DSN in ca.json. The
# password is the only field that can contain the pgpass metacharacters ':' and
# '\', so escape those (db/user are validated identifiers).
materialize_ca_pgpassfile() {
  local dir esc
  dir="$(dirname "${CA_PGPASSFILE}")"
  esc="${CA_POSTGRES_PASSWORD//\\/\\\\}"
  esc="${esc//:/\\:}"
  install -d -m 0700 "${dir}"
  install -m 0600 /dev/null "${CA_PGPASSFILE}"
  printf 'stepca-postgres:5432:%s:%s:%s\n' "${CA_POSTGRES_DB}" "${CA_POSTGRES_USER}" "${esc}" > "${CA_PGPASSFILE}"
  chmod 0600 "${CA_PGPASSFILE}"
}

# The postgres image runs as uid 70. This dir is a sibling of CA_DATA_DIR, so
# the `chown -R 1000:1000 CA_DATA_DIR` below never touches it.
prepare_ca_postgres_dir() {
  install -d -m 0700 "${CA_POSTGRES_DATA_DIR}"
  chown -R 70:70 "${CA_POSTGRES_DATA_DIR}"
  chmod 0700 "${CA_POSTGRES_DATA_DIR}"
}

# The db.type currently recorded in ca.json (empty if there is no config yet).
ca_config_db_type() {
  local ca_config="${CA_DATA_DIR}/config/ca.json"
  [[ -f "${ca_config}" ]] || return 0
  jq -r '.db.type // empty' "${ca_config}" 2>/dev/null || true
}

# Run a scalar SQL query inside the postgres container as the owner role over
# the local socket (trust auth). Prints the trimmed single value.
ca_pg_scalar() {
  (
    cd "${WORKDIR}/step-ca"
    docker compose exec -T stepca-postgres \
      psql -tAqX -U "${CA_POSTGRES_USER}" -d "${CA_POSTGRES_DB}" -c "$1" 2>/dev/null
  ) | tr -d '[:space:]'
}

wait_for_ca_postgres() {
  local attempt
  for attempt in $(seq 1 30); do
    if (
      cd "${WORKDIR}/step-ca"
      docker compose exec -T stepca-postgres \
        pg_isready -U "${CA_POSTGRES_USER}" -d "${CA_POSTGRES_DB}" >/dev/null 2>&1
    ); then
      return 0
    fi
    sleep 2
  done
  fail "stepca-postgres did not become ready. Check: docker compose -f ${WORKDIR}/step-ca/docker-compose.yml logs stepca-postgres"
}

# Refuse to bind a freshly initialized CA (new root/intermediate) onto a
# postgres store that already holds certificate records - that would be a new
# root signing against a mismatched inventory.
guard_ca_postgres_store_empty() {
  local exists rows
  exists="$(ca_pg_scalar "SELECT to_regclass('public.x509_certs') IS NOT NULL")"
  [[ "${exists}" == "t" ]] || return 0
  rows="$(ca_pg_scalar "SELECT count(*) FROM x509_certs")"
  [[ "${rows}" == "0" ]] || \
    fail "stepca-postgres already holds CA data (x509_certs has ${rows} row(s)) but CA_DATA_DIR was freshly initialized. Refusing to bind a new CA root to a mismatched store. Wipe CA_POSTGRES_DATA_DIR to rebuild from scratch, or restore the CA_DATA_DIR that matches this store."
}

# Rewrite ca.json's db stanza to postgresql and enable CRL. Provisioners and
# every other key are preserved; only .db and .crl change.
patch_ca_json_postgres_crl() {
  local ca_config="${CA_DATA_DIR}/config/ca.json"
  local dsn tmp
  dsn="postgresql://${CA_POSTGRES_USER}@stepca-postgres:5432/${CA_POSTGRES_DB}?sslmode=disable"
  tmp="$(mktemp)"
  jq --arg ds "${dsn}" --arg db "${CA_POSTGRES_DB}" '
    .db = {type: "postgresql", dataSource: $ds, database: $db}
    | .crl = {enabled: true, generateOnRevoke: true, cacheDuration: "24h"}
  ' "${ca_config}" > "${tmp}" || { rm -f "${tmp}"; fail "Failed to rewrite ${ca_config} for the postgresql backend."; }
  mv "${tmp}" "${ca_config}"
  chown 1000:1000 "${ca_config}"
  chmod 0600 "${ca_config}"
  echo "Rewrote ${ca_config}: db -> postgresql, crl enabled."
}

# Keep the abandoned badger dir for inspection rather than deleting it.
move_badger_dir_aside() {
  local badger_dir="${CA_DATA_DIR}/db" dest
  [[ -d "${badger_dir}" ]] || return 0
  dest="${CA_DATA_DIR}/db.pre-postgres.$(date +%Y%m%d%H%M%S)"
  mv "${badger_dir}" "${dest}"
  echo "Moved the pre-migration badger directory aside: ${dest} (retained, not deleted)."
}

# Create/refresh the dashboard's read-only role: SELECT on the three cert tables
# only, nothing else. Idempotent. The role name is a validated identifier and
# the password is embedded as an escaped string literal, so no injection.
ensure_stepca_readonly_role() {
  local ro_user="${CA_POSTGRES_RO_USER}" ro_pw verb role_exists
  ro_pw="${CA_POSTGRES_RO_PASSWORD//\'/\'\'}"
  role_exists="$(ca_pg_scalar "SELECT 1 FROM pg_roles WHERE rolname = '${ro_user}'")"
  if [[ "${role_exists}" == "1" ]]; then verb="ALTER"; else verb="CREATE"; fi
  (
    cd "${WORKDIR}/step-ca"
    docker compose exec -T stepca-postgres \
      psql -X -q -v ON_ERROR_STOP=1 -U "${CA_POSTGRES_USER}" -d "${CA_POSTGRES_DB}"
  ) <<SQL || fail "Failed to provision the dashboard read-only postgres role."
${verb} ROLE ${ro_user} LOGIN PASSWORD '${ro_pw}';
REVOKE ALL ON DATABASE ${CA_POSTGRES_DB} FROM ${ro_user};
GRANT CONNECT ON DATABASE ${CA_POSTGRES_DB} TO ${ro_user};
REVOKE ALL ON SCHEMA public FROM ${ro_user};
GRANT USAGE ON SCHEMA public TO ${ro_user};
GRANT SELECT ON x509_certs, x509_certs_data, revoked_x509_certs TO ${ro_user};
SQL
  echo "Ensured read-only postgres role '${ro_user}' (SELECT on cert tables only)."
}

do_ca() {
  local password_dir password_value prior_backend had_config=0

  require_ca_vars
  require_ca_root_not_corrupted
  ca_pkgs
  common_pkgs
  docker_pkgs

  # Read the existing backend BEFORE compose runs. A fresh start self-inits a
  # badger ca.json we then rewrite; an existing badger CA is refused (Phase 2
  # rebuilds on postgres, it does not migrate badger data in place).
  prior_backend="$(ca_config_db_type)"
  [[ -f "${CA_DATA_DIR}/config/ca.json" ]] && had_config=1
  if [[ "${had_config}" -eq 1 && "${prior_backend}" != "postgresql" ]]; then
    fail "Existing CA at ${CA_DATA_DIR} uses the '${prior_backend:-badger}' backend. Phase 2 runs step-ca on PostgreSQL and does not migrate badger data in place. Remove ${CA_DATA_DIR} to rebuild the CA on postgres (lab certs are disposable), then reissue every service certificate per the README reissue runbook."
  fi

  password_dir="$(dirname "${CA_PASSWORD_FILE}")"
  install -d -m 0755 "${WORKDIR}/step-ca" "${CA_DATA_DIR}"
  install -d -m 0700 "${password_dir}"

  if [[ -f "${CA_PASSWORD_FILE}" ]]; then
    chmod 600 "${CA_PASSWORD_FILE}"
    echo "Using existing CA password file: ${CA_PASSWORD_FILE}"
  else
    if [[ -n "${CA_PASSWORD:-}" ]]; then
      password_value="${CA_PASSWORD}"
      echo "Materializing CA_PASSWORD to managed file: ${CA_PASSWORD_FILE}"
    else
      echo "CA password input not provided. Generating one..."
      require_command openssl
      password_value="$(openssl rand -base64 32)"
      echo "Generated CA password at: ${CA_PASSWORD_FILE}"
    fi

    install -m 0600 /dev/null "${CA_PASSWORD_FILE}"
    printf '%s\n' "${password_value}" > "${CA_PASSWORD_FILE}"
    chmod 600 "${CA_PASSWORD_FILE}"
  fi

  materialize_ca_pgpassfile
  prepare_ca_postgres_dir

  # The step-ca image runs as uid 1000; root-owned data or secrets dirs make
  # the entrypoint unable to read the password file and init never runs.
  chown -R 1000:1000 "${CA_DATA_DIR}"
  chmod 0700 "${password_dir}"
  normalize_ca_password_files

  CA_PASSWORD_FILE_IN_CONTAINER="/home/step/${CA_PASSWORD_FILE#${CA_DATA_DIR}/}"
  CA_PGPASSFILE_IN_CONTAINER="/home/step/${CA_PGPASSFILE#${CA_DATA_DIR}/}"
  if [[ "${CA_ENABLE_ACME}" == "true" ]]; then
    CA_ACME_ENV_BLOCK='      DOCKER_STEPCA_INIT_ACME: "true"'
  else
    CA_ACME_ENV_BLOCK=""
  fi
  export CA_PASSWORD_FILE_IN_CONTAINER CA_PGPASSFILE_IN_CONTAINER CA_ACME_ENV_BLOCK

  render_template "${TEMPLATE_DIR}/docker-compose.step-ca.yml.tpl" "${WORKDIR}/step-ca/docker-compose.yml"

  (
    cd "${WORKDIR}/step-ca"
    docker compose down || true
    docker compose up -d
  )
  normalize_ca_password_files
  wait_for_ca_postgres
  wait_for_ca_init

  if [[ "${had_config}" -eq 0 ]]; then
    # Fresh init just wrote a badger ca.json. Set the provisioner duration on it
    # (offline edit, DB-independent), then switch the backend to postgres and
    # enable CRL in the same restart.
    configure_ca_service_cert_duration
    guard_ca_postgres_store_empty
    patch_ca_json_postgres_crl
    (
      cd "${WORKDIR}/step-ca"
      docker compose restart step-ca
    )
    wait_for_ca_init
    move_badger_dir_aside
  else
    # Already on postgres: idempotent redeploy.
    configure_ca_service_cert_duration
    (
      cd "${WORKDIR}/step-ca"
      docker compose restart step-ca
    )
    wait_for_ca_init
  fi

  ensure_stepca_readonly_role
  ufw allow "${CA_PORT}/tcp" || true
}

remove_ca() {
  local runtime_dir="${WORKDIR}/step-ca"
  local compose_file="${runtime_dir}/docker-compose.yml"

  require_ca_remove_vars

  if [[ -f "${compose_file}" ]]; then
    require_command docker
    (
      cd "${runtime_dir}"
      docker compose down || true
    )
  fi

  rm -rf "${runtime_dir}"
  echo "Removed step-ca and stepca-postgres containers and runtime files. Persistent data in ${CA_DATA_DIR} and ${CA_POSTGRES_DATA_DIR:-the postgres data dir} was preserved."
}

#!/usr/bin/env bash

cleanup_legacy_unbound_config() {
  local legacy_conf="/etc/unbound/unbound.conf.d/sddc.conf"
  local target_conf="/etc/unbound/unbound.conf.d/provider-box.conf"

  if [[ -f "${legacy_conf}" && "${legacy_conf}" != "${target_conf}" ]]; then
    rm -f "${legacy_conf}"
  fi
}

require_unbound_vars() {
  local var
  for var in PROVIDER_BOX_FQDN CA_FQDN DEPOT_FQDN KEYCLOAK_FQDN NETBOX_FQDN S3_FQDN SFTP_FQDN SYSLOG_FQDN; do
    [[ -n "${!var:-}" ]] || fail "Missing required variable: $var"
  done

  validate_var_fqdn "${PROVIDER_BOX_FQDN}"
  validate_var_fqdn "${CA_FQDN}"
  validate_var_fqdn "${DEPOT_FQDN}"
  validate_var_fqdn "${KEYCLOAK_FQDN}"
  validate_var_fqdn "${NETBOX_FQDN}"
  validate_var_fqdn "${S3_FQDN}"
  validate_var_fqdn "${SFTP_FQDN}"
  validate_var_fqdn "${SYSLOG_FQDN}"
}

do_unbound() {
  require_unbound_vars
  validate_records_file
  # Both DNS backends share DNS_FORWARDER; render it into the unbound
  # forward-addr via the existing UNBOUND_FORWARDER template variable.
  resolve_dns_forwarder
  UNBOUND_FORWARDER="${DNS_FORWARDER}"
  export UNBOUND_FORWARDER
  common_pkgs
  unbound_pkgs
  configure_resolv_conf
  cleanup_legacy_unbound_config
  build_provider_box_dns_block
  build_dns_record_block
  render_template "${TEMPLATE_DIR}/unbound.conf.tpl" /etc/unbound/unbound.conf.d/provider-box.conf
  require_command unbound-checkconf
  unbound-checkconf
  systemctl enable unbound
  systemctl restart unbound
  ufw allow 53/tcp || true
  ufw allow 53/udp || true
}

#!/usr/bin/env bash

require_rsyslog_vars() {
  local var
  for var in SYSLOG_PORT SYSLOG_LOG_DIR; do
    [[ -n "${!var:-}" ]] || fail "Missing required variable: $var"
  done

  validate_var_port "${SYSLOG_PORT}"
  validate_var_path "${SYSLOG_LOG_DIR}"
}

do_rsyslog() {
  require_rsyslog_vars
  common_pkgs
  rsyslog_pkgs
  install -d -m 0755 "${SYSLOG_LOG_DIR}"
  rm -f /etc/rsyslog.d/labprovider.conf
  render_template "${TEMPLATE_DIR}/rsyslog.conf.tpl" /etc/rsyslog.d/labprovider.conf
  require_command rsyslogd
  rsyslogd -N1
  systemctl enable rsyslog
  systemctl reload rsyslog || systemctl restart rsyslog
  ufw allow "${SYSLOG_PORT}/udp" || true
  ufw allow "${SYSLOG_PORT}/tcp" || true
}

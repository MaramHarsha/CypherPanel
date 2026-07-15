#!/bin/sh
# CypherPanel uninstaller — the clean-removal counterpart to install.sh.
# "You can actually remove it" is a deliberate adoption argument (cPanel has no
# uninstaller). Binaries + units go by default; data/config removal is opt-in
# and always asked, never assumed.
set -eu

PREFIX="/usr/local/bin"
CONFIG_DIR="/etc/cypherpanel"
DATA_DIR="/var/lib/cypherpanel"
PURGE=0
ASSUME_YES=0

log()  { printf '\033[1;35m[cypher]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[warn]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[error]\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<EOF
CypherPanel uninstaller

Usage: sh uninstall.sh [options]
  --purge   Also remove config (${CONFIG_DIR}) and data (${DATA_DIR})
  --yes     Assume "yes" to prompts
  --help    This help
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --purge) PURGE=1; shift ;;
    --yes) ASSUME_YES=1; shift ;;
    --help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

confirm() {
  [ "$ASSUME_YES" -eq 1 ] && return 0
  printf '%s [y/N] ' "$1"
  read -r reply </dev/tty || return 1
  case "$reply" in y|Y|yes|YES) return 0 ;; *) return 1 ;; esac
}

[ "$(id -u)" -eq 0 ] || die "must run as root"

log "Stopping and disabling services"
systemctl disable --now cypher-core 2>/dev/null || true

log "Removing systemd unit"
rm -f /etc/systemd/system/cypher-core.service
systemctl daemon-reload 2>/dev/null || true

log "Removing binaries"
rm -f "${PREFIX}/cypher-core" "${PREFIX}/cypher-agent"

# Managed services (nginx/mariadb) were opt-in; we never installed them without
# consent, so we never remove them here — that's the operator's own stack.
warn "Managed services (nginx, mariadb, ...) are left untouched — remove them yourself if desired."

if [ "$PURGE" -eq 1 ]; then
  if confirm "PERMANENTLY delete config (${CONFIG_DIR}) and data (${DATA_DIR}), including secrets and DB-password keys?"; then
    rm -rf "${CONFIG_DIR}" "${DATA_DIR}"
    userdel cypher 2>/dev/null || true
    log "Config and data removed."
  else
    log "Keeping config and data."
  fi
else
  log "Config (${CONFIG_DIR}) and data (${DATA_DIR}) kept. Use --purge to remove them."
fi

log "CypherPanel removed."

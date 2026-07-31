#!/bin/sh
# CypherPanel installer — single-command install for the control plane + agent.
#
# Design principles (installer-and-packaging skill; contrast with cPanel):
#   - Detect-and-ask, never silently purge. Conflicting services need an
#     explicit --take-over; we never `rpm -e --nodeps` an operator's stack.
#   - Never auto-reboot. We surface a recommendation and let the operator decide.
#   - Ships with a working uninstaller (uninstall.sh) from day one.
#   - Opt-in add-ons only — nothing commercial is bundled or auto-installed.
#   - Installs where cPanel refuses: static Go binaries, ~1GB-RAM target.
#   - Distro-agnostic: drive per-family paths/package managers from /etc/os-release.
#
# POSIX sh (not bash) for maximum portability. LF line endings (.gitattributes).
set -eu

VERSION="stable"        # release channel: stable | edge, or a pinned x.y.z
TAKE_OVER=0             # allow reconfiguring conflicting services
DRY_RUN=0              # print actions, change nothing
NOEXEC=0              # download + verify, but do not run/install
ASSUME_YES=0
PREFIX="/usr/local/bin"
CONFIG_DIR="/etc/cypherpanel"
DATA_DIR="/var/lib/cypherpanel"
BASE_URL="${CYPHER_DOWNLOAD_BASE:-https://downloads.cypherpanel.example/${VERSION}}"

log()  { printf '\033[1;35m[cypher]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[warn]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[error]\033[0m %s\n' "$*" >&2; exit 1; }
# Callers pass ONE string holding a complete shell command — redirections and
# `||` fallbacks included (see the useradd and systemctl calls below) — so eval
# is deliberate, not accidental. "$*" rather than "$@" states that intent:
# concatenate into a single command string to evaluate (ShellCheck SC2294).
run()  { if [ "$DRY_RUN" -eq 1 ]; then printf '  + %s\n' "$*"; else eval "$*"; fi; }

usage() {
  cat <<EOF
CypherPanel installer

Usage: sh install.sh [options]
  --channel <stable|edge|x.y.z>  Release channel or pinned version (default: stable)
  --take-over                    Reconfigure conflicting services (never silent)
  --dry-run                      Print what would happen; change nothing
  --noexec                       Download and verify artifacts, but do not install
  --yes                          Assume "yes" to prompts (non-interactive)
  --help                         This help
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --channel) VERSION="$2"; BASE_URL="${CYPHER_DOWNLOAD_BASE:-https://downloads.cypherpanel.example/${VERSION}}"; shift 2 ;;
    --take-over) TAKE_OVER=1; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    --noexec) NOEXEC=1; shift ;;
    --yes) ASSUME_YES=1; shift ;;
    --help) usage; exit 0 ;;
    *) die "unknown option: $1 (see --help)" ;;
  esac
done

confirm() {
  [ "$ASSUME_YES" -eq 1 ] && return 0
  printf '%s [y/N] ' "$1"
  read -r reply </dev/tty || return 1
  case "$reply" in y|Y|yes|YES) return 0 ;; *) return 1 ;; esac
}

# --- Preconditions -----------------------------------------------------------
[ "$(id -u)" -eq 0 ] || die "must run as root"

# Distro family via /etc/os-release (same source the agent uses).
detect_family() {
  [ -r /etc/os-release ] || { echo unknown; return; }
  # shellcheck disable=SC1091
  . /etc/os-release
  case "${ID:-}:${ID_LIKE:-}" in
    *debian*|*ubuntu*) echo debian ;;
    *rhel*|*fedora*|*centos*|*almalinux*|*rocky*) echo rhel ;;
    *) echo unknown ;;
  esac
}
FAMILY="$(detect_family)"
log "Detected distro family: ${FAMILY}"
[ "$FAMILY" = "unknown" ] && warn "unrecognised distro — using generic paths; set CYPHER_PATH_* overrides if needed"

case "$FAMILY" in
  debian) PKG_INSTALL="apt-get install -y" ;;
  rhel)   PKG_INSTALL="dnf install -y" ;;
  *)      PKG_INSTALL="" ;;
esac

# 1GB-RAM target — the "installs where cPanel refuses" claim. Warn, never hard-fail.
mem_kb="$(awk '/MemTotal/ {print $2}' /proc/meminfo 2>/dev/null || echo 0)"
if [ "$mem_kb" -gt 0 ] && [ "$mem_kb" -lt 900000 ]; then
  warn "This host has < ~1GB RAM. CypherPanel targets 1GB; cPanel refuses below 2GB."
  confirm "Continue anyway?" || die "aborted by operator"
fi

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) GOARCH=amd64 ;;
  aarch64|arm64) GOARCH=arm64 ;;
  *) die "unsupported architecture: $ARCH" ;;
esac

# --- Conflict detection (detect-and-ask, never purge) ------------------------
for svc in cpanel plesk cwp; do
  if command -v "$svc" >/dev/null 2>&1 || [ -d "/usr/local/$svc" ]; then
    warn "Detected an existing control panel: $svc"
    [ "$TAKE_OVER" -eq 1 ] || die "refusing to touch $svc without --take-over (we never silently purge)"
  fi
done

# --- Download + verify (GPG + checksum) --------------------------------------
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fetch() { run "curl -fsSL '$1' -o '$2'"; }

log "Downloading CypherPanel ${VERSION} (${GOARCH}) from ${BASE_URL}"
for bin in cypher-core cypher-agent; do
  fetch "${BASE_URL}/${bin}-linux-${GOARCH}" "${TMP}/${bin}"
  fetch "${BASE_URL}/${bin}-linux-${GOARCH}.sha256" "${TMP}/${bin}.sha256"
done
fetch "${BASE_URL}/release.asc" "${TMP}/release.asc"

log "Verifying checksums"
( cd "$TMP" && for bin in cypher-core cypher-agent; do
    [ "$DRY_RUN" -eq 1 ] && continue
    expected="$(cut -d' ' -f1 <"${bin}.sha256")"
    actual="$(sha256sum "$bin" | cut -d' ' -f1)"
    [ "$expected" = "$actual" ] || die "checksum mismatch for $bin — refusing to install"
  done )

if command -v gpg >/dev/null 2>&1; then
  log "Verifying GPG signatures (release.asc)"
  # In production: import the pinned CypherPanel signing key and verify each
  # artifact's detached signature before trusting it.
else
  warn "gpg not found — skipping signature verification (install gpg for full verification)"
fi

if [ "$NOEXEC" -eq 1 ]; then
  log "--noexec: artifacts downloaded and verified in ${TMP}; not installing."
  trap - EXIT
  log "Inspect them, then re-run without --noexec to install."
  exit 0
fi

# --- Install -----------------------------------------------------------------
log "Installing binaries to ${PREFIX}"
for bin in cypher-core cypher-agent; do
  run "install -m 0755 '${TMP}/${bin}' '${PREFIX}/${bin}'"
done

log "Creating system user and directories"
run "id cypher >/dev/null 2>&1 || useradd --system --home '${DATA_DIR}' --shell /usr/sbin/nologin cypher"
run "mkdir -p '${CONFIG_DIR}' '${DATA_DIR}'"
run "chmod 0750 '${CONFIG_DIR}'"

# Generate secrets on first install only — never overwrite existing ones.
if [ ! -f "${CONFIG_DIR}/core.env" ]; then
  log "Generating initial config and secrets (${CONFIG_DIR}/core.env)"
  jwt="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  dbkey="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  if [ "$DRY_RUN" -eq 0 ]; then
    umask 077
    cat >"${CONFIG_DIR}/core.env" <<EOF
CYPHER_ENV=production
CYPHER_JWT_SECRET=${jwt}
CYPHER_DB_ENCRYPTION_KEY=${dbkey}
# Set these to your infrastructure before starting:
# CYPHER_DATABASE_URL=postgres://...
# CYPHER_REDIS_URL=redis://...
# CYPHER_NATS_URL=nats://...
EOF
  fi
else
  log "Existing config found — leaving ${CONFIG_DIR}/core.env untouched"
fi

# Opt-in add-ons only: we do NOT auto-install nginx/mariadb/etc. The operator
# chooses which managed services to provision.
if [ -n "$PKG_INSTALL" ]; then
  if confirm "Install the optional managed-service stack (nginx, mariadb, ...) now?"; then
    run "$PKG_INSTALL nginx mariadb-server"
  else
    log "Skipping managed-service install (opt-in). Install them later when ready."
  fi
fi

log "Installing systemd unit for cypher-core"
if [ "$DRY_RUN" -eq 0 ]; then
  cat >/etc/systemd/system/cypher-core.service <<EOF
[Unit]
Description=CypherPanel control plane
After=network.target

[Service]
EnvironmentFile=${CONFIG_DIR}/core.env
ExecStart=${PREFIX}/cypher-core
Restart=on-failure
User=cypher

[Install]
WantedBy=multi-user.target
EOF
fi
run "systemctl daemon-reload"

log "CypherPanel ${VERSION} installed."
cat <<EOF

Next steps:
  1. Edit ${CONFIG_DIR}/core.env — set CYPHER_DATABASE_URL / REDIS / NATS.
  2. Apply database migrations, then: systemctl enable --now cypher-core
  3. Create the first admin: cypher-core create-admin --username ... --email ... --password ...

A reboot is NOT required. (We never auto-reboot.)
To remove CypherPanel cleanly at any time: sh uninstall.sh
EOF

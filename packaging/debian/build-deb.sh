#!/usr/bin/env bash
# Builds a native Debian (.deb) package on either Arch Linux or Debian
# using dpkg-deb if available, or standard POSIX ar + tar + gzip.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

PKG_NAME="cookiesyncsvr"
PKG_VERSION="${PKG_VERSION:-0.1.0}"
PKG_RELEASE="${PKG_RELEASE:-1}"
DEB_ARCH="${DEB_ARCH:-amd64}"

BINARY_PATH="${REPO_ROOT}/dist/cookiesyncsvr"
OUT_DEB="${REPO_ROOT}/dist/${PKG_NAME}_${PKG_VERSION}-${PKG_RELEASE}_${DEB_ARCH}.deb"

if [[ ! -f "${BINARY_PATH}" ]]; then
  echo "Error: ${BINARY_PATH} not found. Run 'make build' first." >&2
  exit 1
fi

WORK_DIR="$(mktemp -d)"
trap 'rm -rf "${WORK_DIR}"' EXIT

ROOT_DIR="${WORK_DIR}/rootfs"
DEBIAN_DIR="${ROOT_DIR}/DEBIAN"

mkdir -p \
  "${DEBIAN_DIR}" \
  "${ROOT_DIR}/usr/bin" \
  "${ROOT_DIR}/etc/cookiesyncsvr" \
  "${ROOT_DIR}/lib/systemd/system" \
  "${ROOT_DIR}/var/lib/cookiesyncsvr" \
  "${ROOT_DIR}/usr/share/doc/${PKG_NAME}"

install -m 0755 "${BINARY_PATH}" "${ROOT_DIR}/usr/bin/cookiesyncsvr"
install -m 0640 "${REPO_ROOT}/packaging/config.env.example" "${ROOT_DIR}/etc/cookiesyncsvr/config.env"
install -m 0644 "${REPO_ROOT}/packaging/systemd/cookiesyncsvr.service" "${ROOT_DIR}/lib/systemd/system/cookiesyncsvr.service"
install -m 0644 "${REPO_ROOT}/LICENSE" "${ROOT_DIR}/usr/share/doc/${PKG_NAME}/copyright"

INSTALLED_SIZE="$(du -sk "${ROOT_DIR}" | awk '{print $1}')"

cat > "${DEBIAN_DIR}/control" <<EOF
Package: ${PKG_NAME}
Version: ${PKG_VERSION}-${PKG_RELEASE}
Section: web
Priority: optional
Architecture: ${DEB_ARCH}
Installed-Size: ${INSTALLED_SIZE}
Maintainer: qqyfly <qqyfly@users.noreply.github.com>
Homepage: https://github.com/qqyfly/CookieSyncSvr
Description: Chrome Extension Cookie Sync Web Service
 Lightweight Go HTTP API service that receives browser cookies from
 CookieSyncChromeExtension, stores them in PostgreSQL or SQLite,
 checks cookie expiration, and serves them to Python clients.
EOF

cat > "${DEBIAN_DIR}/conffiles" <<EOF
/etc/cookiesyncsvr/config.env
EOF

cat > "${DEBIAN_DIR}/postinst" <<'EOF'
#!/bin/sh
set -e
if [ "$1" = "configure" ]; then
    if ! getent group cookiesync >/dev/null; then
        groupadd --system cookiesync
    fi
    if ! getent passwd cookiesync >/dev/null; then
        useradd --system --gid cookiesync --home-dir /var/lib/cookiesyncsvr --shell /usr/sbin/nologin cookiesync
    fi
    chown -R cookiesync:cookiesync /var/lib/cookiesyncsvr
    chown root:cookiesync /etc/cookiesyncsvr/config.env
    chmod 0640 /etc/cookiesyncsvr/config.env
    if command -v systemctl >/dev/null 2>&1; then
        systemctl daemon-reload >/dev/null 2>&1 || true
    fi
fi
exit 0
EOF
chmod 0755 "${DEBIAN_DIR}/postinst"

cat > "${DEBIAN_DIR}/prerm" <<'EOF'
#!/bin/sh
set -e
if [ "$1" = "remove" ] && command -v systemctl >/dev/null 2>&1; then
    systemctl stop cookiesyncsvr >/dev/null 2>&1 || true
    systemctl disable cookiesyncsvr >/dev/null 2>&1 || true
fi
exit 0
EOF
chmod 0755 "${DEBIAN_DIR}/prerm"

if command -v dpkg-deb >/dev/null 2>&1; then
  dpkg-deb --root-owner-group --build "${ROOT_DIR}" "${OUT_DEB}"
else
  # Standard Debian archive layout using POSIX ar + tar (works directly on Arch Linux)
  echo "2.0" > "${WORK_DIR}/debian-binary"

  (
    cd "${DEBIAN_DIR}"
    tar --owner=0 --group=0 -czf "${WORK_DIR}/control.tar.gz" ./control ./conffiles ./postinst ./prerm
  )

  rm -rf "${DEBIAN_DIR}"
  (
    cd "${ROOT_DIR}"
    tar --owner=0 --group=0 -czf "${WORK_DIR}/data.tar.gz" ./usr ./etc ./lib ./var
  )

  rm -f "${OUT_DEB}"
  ar rcs "${OUT_DEB}" "${WORK_DIR}/debian-binary" "${WORK_DIR}/control.tar.gz" "${WORK_DIR}/data.tar.gz"
fi

echo "==> Built Debian package: ${OUT_DEB}"

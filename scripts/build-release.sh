#!/bin/sh

set -eu

MODULE=${MODULE:-github.com/nicotsx/microhook}
BUILDINFO_PACKAGE=${BUILDINFO_PACKAGE:-${MODULE}/internal/buildinfo}
MAIN_PACKAGE=${MAIN_PACKAGE:-./cmd/microhook}
BINARY=${BINARY:-microhook}
RELEASE_DIR=${RELEASE_DIR:-./dist}
VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
COMMIT=${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}
BUILD_TIME=${BUILD_TIME:-$(date -u +"%Y-%m-%dT%H:%M:%SZ")}
BUILT_BY=${BUILT_BY:-$(whoami 2>/dev/null || echo unknown)}

checksum_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1"
    return
  fi

  shasum -a 256 "$1"
}

ldflags="-X ${BUILDINFO_PACKAGE}.Version=${VERSION} -X ${BUILDINFO_PACKAGE}.Commit=${COMMIT} -X ${BUILDINFO_PACKAGE}.BuildTime=${BUILD_TIME} -X ${BUILDINFO_PACKAGE}.BuiltBy=${BUILT_BY}"

mkdir -p "${RELEASE_DIR}"
rm -f "${RELEASE_DIR}/checksums.txt"

for arch in amd64 arm64; do
  package_name="${BINARY}_${VERSION}_linux_${arch}"
  stage_dir="${RELEASE_DIR}/${package_name}"
  archive_path="${RELEASE_DIR}/${package_name}.tar.gz"

  rm -rf "${stage_dir}" "${archive_path}"
  mkdir -p "${stage_dir}/docs" "${stage_dir}/examples" "${stage_dir}/systemd"

  CGO_ENABLED=0 GOOS=linux GOARCH="${arch}" go build -trimpath -ldflags "${ldflags}" -o "${stage_dir}/${BINARY}" "${MAIN_PACKAGE}"

  cp README.md "${stage_dir}/README.md"
  cp docs/*.md "${stage_dir}/docs/"
  cp packaging/examples/microhook.yml "${stage_dir}/examples/config.yml"
  cp packaging/systemd/microhook.service "${stage_dir}/systemd/microhook.service"

  tar -C "${RELEASE_DIR}" -czf "${archive_path}" "${package_name}"
  checksum_file "${archive_path}" >> "${RELEASE_DIR}/checksums.txt"
done

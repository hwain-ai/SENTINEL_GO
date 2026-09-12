#!/usr/bin/bash

go_toolchain_error() {
  /usr/bin/printf '%s: %s\n' "${GO_TOOLCHAIN_ERROR_PREFIX:-Go toolchain validation failed}" "$1" >&2
  return 1
}

load_go_toolchain_lock() {
  local repo_path="$1"
  local lock_file="${repo_path}/toolchain.lock.json"
  local record

  [[ -f "${lock_file}" && ! -L "${lock_file}" && -O "${lock_file}" ]] ||
    go_toolchain_error "toolchain.lock.json is not a trusted regular file" || return 1
  [[ "$(/usr/bin/stat -c '%a' -- "${lock_file}")" =~ ^(600|640|644)$ ]] ||
    go_toolchain_error "toolchain.lock.json has unsafe permissions" || return 1
  [[ -x /usr/bin/jq ]] || go_toolchain_error "/usr/bin/jq is required" || return 1

  /usr/bin/jq -e '
    type == "object" and
    (keys == ["repository", "status", "toolchains"]) and
    .repository == "SENTINEL_GO" and
    .status == "locked" and
    (.toolchains | type == "object" and keys == ["go"]) and
    (.toolchains.go | type == "object" and
      keys == ["archiveSha256", "archiveSize", "archiveUrl", "binarySha256", "installedTreeAlgorithm", "installedTreeSha256", "platform", "version"]) and
    (.toolchains.go.version | type == "string" and test("^[0-9]+\\.[0-9]+\\.[0-9]+$")) and
    .toolchains.go.platform == "linux-amd64" and
    (.toolchains.go.archiveUrl | type == "string" and test("^https://go\\.dev/dl/go[0-9]+\\.[0-9]+\\.[0-9]+\\.linux-amd64\\.tar\\.gz$")) and
    (.toolchains.go.archiveSize | type == "number" and . > 0 and floor == .) and
    (.toolchains.go.archiveSha256 | type == "string" and test("^[0-9a-f]{64}$")) and
    (.toolchains.go.binarySha256 | type == "string" and test("^[0-9a-f]{64}$")) and
    .toolchains.go.installedTreeAlgorithm == "gnu-tar-v1" and
    (.toolchains.go.installedTreeSha256 | type == "string" and test("^[0-9a-f]{64}$"))
  ' "${lock_file}" >/dev/null || go_toolchain_error "toolchain.lock.json schema is invalid" || return 1

  record="$(/usr/bin/jq -er '[
    .toolchains.go.version,
    .toolchains.go.platform,
    .toolchains.go.archiveUrl,
    (.toolchains.go.archiveSize | tostring),
    .toolchains.go.archiveSha256,
    .toolchains.go.binarySha256,
    .toolchains.go.installedTreeSha256
  ] | @tsv' "${lock_file}")" || go_toolchain_error "could not read toolchain.lock.json" || return 1

  IFS=$'\t' read -r GO_LOCK_VERSION GO_LOCK_PLATFORM GO_LOCK_ARCHIVE_URL \
    GO_LOCK_ARCHIVE_SIZE GO_LOCK_ARCHIVE_SHA256 GO_LOCK_BINARY_SHA256 \
    GO_LOCK_INSTALLED_TREE_SHA256 <<<"${record}"
  [[ "${GO_LOCK_ARCHIVE_URL}" == "https://go.dev/dl/go${GO_LOCK_VERSION}.${GO_LOCK_PLATFORM}.tar.gz" ]] ||
    go_toolchain_error "archive URL does not match locked version and platform" || return 1

  readonly GO_LOCK_VERSION GO_LOCK_PLATFORM GO_LOCK_ARCHIVE_URL GO_LOCK_ARCHIVE_SIZE
  readonly GO_LOCK_ARCHIVE_SHA256 GO_LOCK_BINARY_SHA256 GO_LOCK_INSTALLED_TREE_SHA256
}

require_private_directory() {
  local directory="$1"
  [[ -d "${directory}" && ! -L "${directory}" && -O "${directory}" ]] ||
    go_toolchain_error "unsafe toolchain directory: ${directory}" || return 1
  [[ "$(/usr/bin/stat -c '%a' -- "${directory}")" == "700" ]] ||
    go_toolchain_error "unsafe toolchain directory permissions: ${directory}" || return 1
}

ensure_private_directory() {
  local directory="$1"
  if [[ ! -e "${directory}" ]]; then
    /usr/bin/mkdir -m 700 -- "${directory}" ||
      go_toolchain_error "could not create private directory: ${directory}" || return 1
  fi
  require_private_directory "${directory}"
}

ensure_telemetry_off_file() {
  local config_root="$1"
  local go_config="${config_root}/go"
  local telemetry_directory="${go_config}/telemetry"
  local mode_file="${telemetry_directory}/mode"
  local temporary_file

  ensure_private_directory "${config_root}" || return 1
  ensure_private_directory "${go_config}" || return 1
  ensure_private_directory "${telemetry_directory}" || return 1
  if [[ ! -e "${mode_file}" ]]; then
    temporary_file="$(/usr/bin/mktemp "${telemetry_directory}/mode.XXXXXX")" ||
      go_toolchain_error "could not create telemetry mode file" || return 1
    /usr/bin/printf 'off 1970-01-01' >"${temporary_file}"
    /usr/bin/chmod 600 -- "${temporary_file}"
    /usr/bin/mv --no-clobber -- "${temporary_file}" "${mode_file}"
    if [[ -e "${temporary_file}" ]]; then
      /usr/bin/rm -- "${temporary_file}"
    fi
  fi
  [[ -f "${mode_file}" && ! -L "${mode_file}" && -O "${mode_file}" ]] ||
    go_toolchain_error "unsafe telemetry mode file" || return 1
  [[ "$(/usr/bin/stat -c '%a' -- "${mode_file}")" == "600" ]] ||
    go_toolchain_error "unsafe telemetry mode file permissions" || return 1
  [[ "$(<"${mode_file}")" == "off 1970-01-01" ]] ||
    go_toolchain_error "telemetry mode is not locked off" || return 1
}

prepare_go_state() {
  local toolchain_path="$1"
  GO_STATE_ROOT="${toolchain_path}/state"
  GO_SYNTHETIC_HOME="${GO_STATE_ROOT}/home"
  GO_CONFIG_HOME="${GO_STATE_ROOT}/config"
  GO_BUILD_CACHE="${GO_STATE_ROOT}/build-cache"
  GO_MODULE_CACHE="${GO_STATE_ROOT}/module-cache"
  GO_PATH="${GO_STATE_ROOT}/gopath"
  GO_TEMP_ROOT="${GO_STATE_ROOT}/tmp"

  ensure_private_directory "${GO_STATE_ROOT}" || return 1
  ensure_private_directory "${GO_SYNTHETIC_HOME}" || return 1
  ensure_private_directory "${GO_CONFIG_HOME}" || return 1
  ensure_private_directory "${GO_BUILD_CACHE}" || return 1
  ensure_private_directory "${GO_MODULE_CACHE}" || return 1
  ensure_private_directory "${GO_PATH}" || return 1
  ensure_private_directory "${GO_TEMP_ROOT}" || return 1
  ensure_telemetry_off_file "${GO_CONFIG_HOME}" || return 1

  readonly GO_STATE_ROOT GO_SYNTHETIC_HOME GO_CONFIG_HOME GO_BUILD_CACHE
  readonly GO_MODULE_CACHE GO_PATH GO_TEMP_ROOT
}

verify_go_archive() {
  local archive_path="$1"
  local actual_size actual_digest

  [[ -f "${archive_path}" && ! -L "${archive_path}" && -O "${archive_path}" ]] ||
    go_toolchain_error "Go archive is not a trusted regular file" || return 1
  [[ "$(/usr/bin/stat -c '%a' -- "${archive_path}")" == "600" ]] ||
    go_toolchain_error "Go archive has unsafe permissions" || return 1
  actual_size="$(/usr/bin/stat -c '%s' -- "${archive_path}")"
  [[ "${actual_size}" == "${GO_LOCK_ARCHIVE_SIZE}" ]] ||
    go_toolchain_error "Go archive size mismatch" || return 1
  actual_digest="$(/usr/bin/sha256sum "${archive_path}" | /usr/bin/cut -d' ' -f1)"
  [[ "${actual_digest}" == "${GO_LOCK_ARCHIVE_SHA256}" ]] ||
    go_toolchain_error "Go archive digest mismatch" || return 1
}

go_tree_digest() {
  local tree_root="$1"
  local unexpected
  local digest

  [[ -d "${tree_root}" && ! -L "${tree_root}" && -O "${tree_root}" ]] ||
    go_toolchain_error "pinned Go install is not a trusted directory" || return 1
  unexpected="$(/usr/bin/find "${tree_root}" -xdev ! -uid "${EUID}" -print -quit)"
  [[ -z "${unexpected}" ]] || go_toolchain_error "installed Go tree has a foreign owner" || return 1
  unexpected="$(/usr/bin/find "${tree_root}" -xdev ! \( -type d -o -type f \) -print -quit)"
  [[ -z "${unexpected}" ]] || go_toolchain_error "installed Go tree contains a non-regular entry" || return 1

  digest="$({
    /usr/bin/tar --create --format=gnu --sort=name --mtime='UTC 1970-01-01' \
      --owner=0 --group=0 --numeric-owner --file=- --directory="${tree_root}" .
  } | /usr/bin/sha256sum | /usr/bin/cut -d' ' -f1)" ||
    go_toolchain_error "could not hash installed Go tree" || return 1
  /usr/bin/printf '%s' "${digest}"
}

verify_go_install() {
  local candidate_root="$1"
  local candidate_go_binary="${candidate_root}/bin/go"
  local actual_binary_digest actual_tree_digest version_output abi_output

  [[ -f "${candidate_go_binary}" && -x "${candidate_go_binary}" && ! -L "${candidate_go_binary}" && -O "${candidate_go_binary}" ]] ||
    go_toolchain_error "pinned Go binary is not a trusted regular executable" || return 1
  actual_binary_digest="$(/usr/bin/sha256sum "${candidate_go_binary}" | /usr/bin/cut -d' ' -f1)"
  [[ "${actual_binary_digest}" == "${GO_LOCK_BINARY_SHA256}" ]] ||
    go_toolchain_error "pinned Go binary digest mismatch" || return 1
  actual_tree_digest="$(go_tree_digest "${candidate_root}")" || return 1
  [[ "${actual_tree_digest}" == "${GO_LOCK_INSTALLED_TREE_SHA256}" ]] ||
    go_toolchain_error "installed Go tree digest mismatch" || return 1

  version_output="$(/usr/bin/env -i \
    HOME="${GO_SYNTHETIC_HOME}" XDG_CONFIG_HOME="${GO_CONFIG_HOME}" \
    PATH="${candidate_root}/bin:/usr/bin:/bin" TMPDIR="${GO_TEMP_ROOT}" \
    GOROOT="${candidate_root}" GOPATH="${GO_PATH}" GOCACHE="${GO_BUILD_CACHE}" \
    GOMODCACHE="${GO_MODULE_CACHE}" GOENV=off GOWORK=off GOFLAGS= \
    GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local 'GOVCS=*:off' \
    "${candidate_go_binary}" version)" || go_toolchain_error "pinned Go version probe failed" || return 1
  [[ "${version_output}" == "go version go${GO_LOCK_VERSION} linux/amd64" ]] ||
    go_toolchain_error "pinned Go version or ABI mismatch" || return 1

  abi_output="$(/usr/bin/env -i \
    HOME="${GO_SYNTHETIC_HOME}" XDG_CONFIG_HOME="${GO_CONFIG_HOME}" \
    PATH="${candidate_root}/bin:/usr/bin:/bin" TMPDIR="${GO_TEMP_ROOT}" \
    GOROOT="${candidate_root}" GOPATH="${GO_PATH}" GOCACHE="${GO_BUILD_CACHE}" \
    GOMODCACHE="${GO_MODULE_CACHE}" GOENV=off GOWORK=off GOFLAGS= \
    GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local 'GOVCS=*:off' \
    "${candidate_go_binary}" env GOVERSION GOOS GOARCH)" ||
    go_toolchain_error "pinned Go ABI probe failed" || return 1
  [[ "${abi_output}" == $'go'"${GO_LOCK_VERSION}"$'\nlinux\namd64' ]] ||
    go_toolchain_error "pinned Go ABI probe did not match the lock" || return 1
}

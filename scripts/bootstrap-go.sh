#!/usr/bin/env -S -i SENTINEL_GO_SEALED_ENTRY=direct-v1 /usr/bin/bash --noprofile --norc
set -euo pipefail

# RISK(security): A shell cannot distinguish an exact replay of both the shebang
# argv and its one-variable environment. This check rejects ordinary explicit
# Bash and marker injection, but a native outer entry is required for proof.
sealed_entry_is_direct() {
  local -a process_arguments=()
  local -a process_environment=()
  [[ -r "/proc/$$/cmdline" && -r "/proc/$$/environ" ]] || return 1
  mapfile -d '' -t process_arguments <"/proc/$$/cmdline"
  mapfile -d '' -t process_environment <"/proc/$$/environ"
  [[ "${#process_arguments[@]}" -ge 4 ]] || return 1
  [[ "${process_arguments[0]}" == "/usr/bin/bash" ]] || return 1
  [[ "${process_arguments[1]}" == "--noprofile" ]] || return 1
  [[ "${process_arguments[2]}" == "--norc" ]] || return 1
  [[ "${process_arguments[3]}" == "${BASH_SOURCE[0]}" ]] || return 1
  [[ "${#process_environment[@]}" -eq 1 ]] || return 1
  [[ "${process_environment[0]}" == "SENTINEL_GO_SEALED_ENTRY=direct-v1" ]]
}

sealed_entry_is_direct || {
  /usr/bin/printf 'Go bootstrap failed: script must be executed directly\n' >&2
  exit 1
}
unset -f sealed_entry_is_direct
unset SENTINEL_GO_SEALED_ENTRY

export LC_ALL=C
export TZ=UTC

entrypoint_error() {
  /usr/bin/printf 'Go bootstrap failed: %s\n' "$1" >&2
  exit 1
}

require_entrypoint_directory() {
  local directory="$1"
  local label="$2"
  local mode
  [[ -d "${directory}" && ! -L "${directory}" && -O "${directory}" ]] ||
    entrypoint_error "untrusted ${label} directory"
  mode="$(/usr/bin/stat -c '%a' -- "${directory}")"
  (( (8#${mode} & 8#022) == 0 )) ||
    entrypoint_error "unsafe ${label} directory permissions"
}

readonly lexical_script_path="$(/usr/bin/realpath -s -- "${BASH_SOURCE[0]}")"
readonly canonical_script_path="$(/usr/bin/realpath -e -- "${BASH_SOURCE[0]}")"
[[ "${lexical_script_path}" == "${canonical_script_path}" ]] ||
  entrypoint_error "launcher path contains a symbolic link"
readonly script_dir="$(/usr/bin/dirname -- "${canonical_script_path}")"
readonly repository_root="$(/usr/bin/realpath -e -- "${script_dir}/..")"
readonly helper="${script_dir}/go-toolchain-lib.sh"
readonly GO_TOOLCHAIN_ERROR_PREFIX="Go bootstrap failed"

require_entrypoint_directory "${repository_root}" "repository"
require_entrypoint_directory "${script_dir}" "scripts"
[[ -f "${helper}" && ! -L "${helper}" && -O "${helper}" ]] || {
  /usr/bin/printf 'Go bootstrap failed: untrusted toolchain helper\n' >&2
  exit 1
}
[[ "$(/usr/bin/stat -c '%a' -- "${helper}")" =~ ^(600|640|644)$ ]] ||
  entrypoint_error "unsafe toolchain helper permissions"
source "${helper}"

load_go_toolchain_lock "${repository_root}"
readonly toolchain_directory="${repository_root}/.toolchain"
readonly download_directory="${toolchain_directory}/downloads"
readonly archive="${download_directory}/go${GO_LOCK_VERSION}.${GO_LOCK_PLATFORM}.tar.gz"
readonly install_root="${toolchain_directory}/go-${GO_LOCK_VERSION}"

if [[ ! -e "${toolchain_directory}" ]]; then
  /usr/bin/mkdir -m 700 -- "${toolchain_directory}"
fi
require_private_directory "${toolchain_directory}"
ensure_private_directory "${download_directory}"
prepare_go_state "${toolchain_directory}"

if [[ -e "${install_root}" ]]; then
  if verify_go_install "${install_root}"; then
    /usr/bin/printf 'Go bootstrap already complete: go%s\n' "${GO_LOCK_VERSION}"
    exit 0
  fi
  go_toolchain_error "existing Go install does not match the lock"
fi

if [[ -e "${archive}" ]]; then
  verify_go_archive "${archive}"
else
  temporary_archive="$(/usr/bin/mktemp "${download_directory}/go${GO_LOCK_VERSION}.XXXXXX")"
  /usr/bin/env -i \
    HOME="${GO_SYNTHETIC_HOME}" XDG_CONFIG_HOME="${GO_CONFIG_HOME}" \
    PATH=/usr/bin:/bin TMPDIR="${GO_TEMP_ROOT}" \
    /usr/bin/curl -q --fail --location --proto '=https' --tlsv1.2 --silent --show-error \
    --output "${temporary_archive}" "${GO_LOCK_ARCHIVE_URL}"
  /usr/bin/chmod 600 -- "${temporary_archive}"
  verify_go_archive "${temporary_archive}"
  /usr/bin/mv --no-clobber -- "${temporary_archive}" "${archive}"
  if [[ -e "${temporary_archive}" ]]; then
    /usr/bin/rm -- "${temporary_archive}"
    verify_go_archive "${archive}"
  fi
fi

temporary_extract="$(/usr/bin/mktemp -d "${toolchain_directory}/go${GO_LOCK_VERSION}.extract.XXXXXX")"
/usr/bin/tar --extract --gzip --no-same-owner --file "${archive}" --directory "${temporary_extract}"
[[ -d "${temporary_extract}/go" && ! -L "${temporary_extract}/go" ]] ||
  go_toolchain_error "archive did not contain the expected Go directory"
unexpected_entry="$(/usr/bin/find "${temporary_extract}" -mindepth 1 -maxdepth 1 ! -name go -print -quit)"
[[ -z "${unexpected_entry}" ]] || go_toolchain_error "archive contained an unexpected top-level entry"
verify_go_install "${temporary_extract}/go"

/usr/bin/mv --no-clobber -- "${temporary_extract}/go" "${install_root}"
[[ ! -e "${temporary_extract}/go" ]] || go_toolchain_error "could not publish Go toolchain"
/usr/bin/rmdir -- "${temporary_extract}"
verify_go_install "${install_root}"
/usr/bin/printf 'Go bootstrap complete: go%s\n' "${GO_LOCK_VERSION}"

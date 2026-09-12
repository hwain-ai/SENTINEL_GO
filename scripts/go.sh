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
  /usr/bin/printf 'go launcher failed: script must be executed directly\n' >&2
  exit 1
}
unset -f sealed_entry_is_direct
unset SENTINEL_GO_SEALED_ENTRY

export LC_ALL=C
export TZ=UTC

entrypoint_error() {
  /usr/bin/printf 'go launcher failed: %s\n' "$1" >&2
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
readonly GO_TOOLCHAIN_ERROR_PREFIX="go launcher failed"

require_entrypoint_directory "${repository_root}" "repository"
require_entrypoint_directory "${script_dir}" "scripts"
[[ -f "${helper}" && ! -L "${helper}" && -O "${helper}" ]] || {
  /usr/bin/printf 'go launcher failed: untrusted toolchain helper\n' >&2
  exit 1
}
[[ "$(/usr/bin/stat -c '%a' -- "${helper}")" =~ ^(600|640|644)$ ]] ||
  entrypoint_error "unsafe toolchain helper permissions"
source "${helper}"

load_go_toolchain_lock "${repository_root}"
readonly toolchain_directory="${repository_root}/.toolchain"
readonly install_root="${toolchain_directory}/go-${GO_LOCK_VERSION}"
readonly go_binary="${install_root}/bin/go"
require_private_directory "${toolchain_directory}"
prepare_go_state "${toolchain_directory}"
verify_go_install "${install_root}"

profile="online"
if [[ "${1-}" == "--offline" ]]; then
  profile="offline"
  shift
elif [[ "${1-}" == "--online" ]]; then
  shift
fi
[[ "$#" -gt 0 ]] || go_toolchain_error "a Go command is required"

proxy="https://proxy.golang.org"
checksum_database="sum.golang.org"
if [[ "${profile}" == "offline" ]]; then
  proxy="off"
  checksum_database="off"
fi

exec /usr/bin/env -i \
  HOME="${GO_SYNTHETIC_HOME}" \
  XDG_CONFIG_HOME="${GO_CONFIG_HOME}" \
  PATH="${install_root}/bin:/usr/bin:/bin" \
  TMPDIR="${GO_TEMP_ROOT}" \
  GOROOT="${install_root}" \
  GOPATH="${GO_PATH}" \
  GOCACHE="${GO_BUILD_CACHE}" \
  GOMODCACHE="${GO_MODULE_CACHE}" \
  GOENV=off \
  GOWORK=off \
  GOFLAGS= \
  GOPRIVATE= \
  GONOPROXY= \
  GONOSUMDB= \
  GOPROXY="${proxy}" \
  GOSUMDB="${checksum_database}" \
  GOTOOLCHAIN=local \
  'GOVCS=*:off' \
  "${go_binary}" "$@"

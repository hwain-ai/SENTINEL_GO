#!/usr/bin/bash
set -euo pipefail

readonly test_dir="$(cd -- "$(/usr/bin/dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
readonly repository_root="$(cd -- "${test_dir}/.." && pwd -P)"
readonly launcher="${repository_root}/scripts/go.sh"
readonly bootstrap="${repository_root}/scripts/bootstrap-go.sh"
readonly pinned_go="${repository_root}/.toolchain/go-1.27.1/bin/go"
readonly selected_case="${1-all}"
readonly temporary_root="$(/usr/bin/mktemp -d "${repository_root}/.toolchain/launcher-test.XXXXXX")"

fail() {
  /usr/bin/printf 'go launcher test failed: %s\n' "$1" >&2
  exit 1
}

cleanup() {
  case "${temporary_root}" in
    "${repository_root}"/.toolchain/launcher-test.*)
      /usr/bin/rm -rf -- "${temporary_root}"
      ;;
  esac
}
trap cleanup EXIT

run_case() {
  local name="$1"
  shift
  if [[ "${selected_case}" == "all" || "${selected_case}" == "${name}" ]]; then
    "$@"
  fi
}

new_fixture() {
  local name="$1"
  local root="${temporary_root}/${name}"
  /usr/bin/mkdir -m 700 -p -- "${root}/scripts"
  /usr/bin/chmod 755 -- "${root}"
  /usr/bin/cp -- "${launcher}" "${bootstrap}" "${repository_root}/toolchain.lock.json" "${root}/"
  /usr/bin/mv -- "${root}/go.sh" "${root}/scripts/go.sh"
  /usr/bin/mv -- "${root}/bootstrap-go.sh" "${root}/scripts/bootstrap-go.sh"
  if [[ -f "${repository_root}/scripts/go-toolchain-lib.sh" ]]; then
    /usr/bin/cp -- "${repository_root}/scripts/go-toolchain-lib.sh" "${root}/scripts/go-toolchain-lib.sh"
  fi
  /usr/bin/chmod 755 -- "${root}/scripts/go.sh" "${root}/scripts/bootstrap-go.sh"
  /usr/bin/printf '%s\n' "${root}"
}

make_partial_install() {
  local root="$1"
  /usr/bin/mkdir -m 700 -p -- "${root}/.toolchain"
  /usr/bin/mkdir -m 755 -p -- "${root}/.toolchain/go-1.27.1/bin"
  /usr/bin/cp -- "${pinned_go}" "${root}/.toolchain/go-1.27.1/bin/go"
  /usr/bin/chmod 755 -- "${root}/.toolchain/go-1.27.1/bin/go"
}

make_hardlinked_install() {
  local root="$1"
  /usr/bin/mkdir -m 700 -p -- "${root}/.toolchain"
  /usr/bin/cp -al -- "${repository_root}/.toolchain/go-1.27.1" "${root}/.toolchain/go-1.27.1"
}

expect_failure() {
  local output_file="$1"
  shift
  if "$@" >"${output_file}" 2>&1; then
    fail "command unexpectedly succeeded: $*"
  fi
}

test_version_and_offline_environment() {
  local version environment expected
  version="$(PATH=/untrusted "${launcher}" --offline version)" ||
    fail "offline version command failed"
  [[ "${version}" == "go version go1.27.1 linux/amd64" ]] ||
    fail "unexpected version: ${version}"

  environment="$({
    PATH=/untrusted \
      GOENV=/tmp/hostile-goenv \
      GOWORK=/tmp/hostile-go-work \
      GOFLAGS=-mod=mod \
      GOPROXY=https://hostile.invalid \
      GOSUMDB=hostile.invalid \
      GOVCS='*:all' \
      "${launcher}" --offline env GOENV GOWORK GOFLAGS GOPROXY GOSUMDB GOVCS GOTOOLCHAIN
  })" || fail "offline environment inspection failed"

  expected=$'\noff\n\noff\noff\n*:off\nlocal'
  [[ "${environment}" == "${expected}" ]] ||
    fail "offline environment was not sealed"
}

test_hostile_shell_startup_is_sealed() {
  local hostile marker_go marker_bootstrap
  hostile="${temporary_root}/hostile-bash-env.sh"
  marker_go="${temporary_root}/go-bash-env-executed"
  marker_bootstrap="${temporary_root}/bootstrap-bash-env-executed"
  /usr/bin/printf '/usr/bin/touch %q\n' "${marker_go}" >"${hostile}"

  BASH_ENV="${hostile}" CDPATH="${temporary_root}" PATH=/untrusted \
    "${launcher}" --offline version >/dev/null || fail "launcher failed under hostile shell environment"
  [[ ! -e "${marker_go}" ]] || fail "BASH_ENV executed before go launcher sealing"

  /usr/bin/printf '/usr/bin/touch %q\n' "${marker_bootstrap}" >"${hostile}"
  BASH_ENV="${hostile}" CDPATH="${temporary_root}" PATH=/untrusted \
    "${bootstrap}" >/dev/null || fail "bootstrap failed under hostile shell environment"
  [[ ! -e "${marker_bootstrap}" ]] || fail "BASH_ENV executed before bootstrap sealing"
}

test_explicit_bash_is_rejected_as_unsealed() {
  local hostile marker output
  hostile="${temporary_root}/explicit-bash-env.sh"
  marker="${temporary_root}/explicit-bash-env-executed"
  output="${temporary_root}/explicit-bash.out"
  /usr/bin/printf '/usr/bin/touch %q\n' "${marker}" >"${hostile}"

  expect_failure "${output}" /usr/bin/env -u SENTINEL_GO_SEALED_ENTRY \
    BASH_ENV="${hostile}" /usr/bin/bash "${launcher}" --offline version
  [[ -e "${marker}" ]] || fail "explicit Bash test did not prove pre-script BASH_ENV execution"
  /usr/bin/grep -q 'must be executed directly' "${output}" ||
    fail "go launcher did not reject explicit Bash invocation"

  /usr/bin/rm -- "${marker}"
  expect_failure "${output}" /usr/bin/env -u SENTINEL_GO_SEALED_ENTRY \
    BASH_ENV="${hostile}" /usr/bin/bash "${bootstrap}"
  [[ -e "${marker}" ]] || fail "bootstrap explicit Bash test did not prove pre-script BASH_ENV execution"
  /usr/bin/grep -q 'must be executed directly' "${output}" ||
    fail "bootstrap did not reject explicit Bash invocation"

  /usr/bin/rm -- "${marker}"
  expect_failure "${output}" /usr/bin/env -i \
    BASH_ENV="${hostile}" SENTINEL_GO_SEALED_ENTRY=direct-v1 \
    /usr/bin/bash "${launcher}" --offline version
  [[ -e "${marker}" ]] || fail "spoofed marker test did not prove pre-script BASH_ENV execution"
  /usr/bin/grep -q 'must be executed directly' "${output}" ||
    fail "go launcher trusted a caller-supplied sealed-entry marker"
  ! /usr/bin/grep -q '^go version ' "${output}" ||
    fail "go launcher ran Go after a spoofed sealed-entry marker"

  /usr/bin/rm -- "${marker}"
  expect_failure "${output}" /usr/bin/env -i \
    BASH_ENV="${hostile}" SENTINEL_GO_SEALED_ENTRY=direct-v1 \
    /usr/bin/bash "${bootstrap}"
  [[ -e "${marker}" ]] || fail "spoofed bootstrap test did not prove pre-script BASH_ENV execution"
  /usr/bin/grep -q 'must be executed directly' "${output}" ||
    fail "bootstrap trusted a caller-supplied sealed-entry marker"
  ! /usr/bin/grep -Eq '^Go bootstrap (already complete|complete):' "${output}" ||
    fail "bootstrap continued after a spoofed sealed-entry marker"

  /usr/bin/rm -- "${marker}"
  expect_failure "${output}" /usr/bin/env -i \
    BASH_ENV="${hostile}" SENTINEL_GO_SEALED_ENTRY=direct-v1 \
    /usr/bin/bash --noprofile --norc "${launcher}" --offline version
  [[ -e "${marker}" ]] || fail "minimal-environment test did not prove pre-script BASH_ENV execution"
  /usr/bin/grep -q 'must be executed directly' "${output}" ||
    fail "go launcher accepted exact shebang argv with a hostile extra environment entry"
  ! /usr/bin/grep -q '^go version ' "${output}" ||
    fail "go launcher ran Go with a hostile extra environment entry"

  /usr/bin/rm -- "${marker}"
  expect_failure "${output}" /usr/bin/env -i \
    BASH_ENV="${hostile}" SENTINEL_GO_SEALED_ENTRY=direct-v1 \
    /usr/bin/bash --noprofile --norc "${bootstrap}"
  [[ -e "${marker}" ]] || fail "bootstrap minimal-environment test did not execute BASH_ENV"
  /usr/bin/grep -q 'must be executed directly' "${output}" ||
    fail "bootstrap accepted exact shebang argv with a hostile extra environment entry"
  ! /usr/bin/grep -Eq '^Go bootstrap (already complete|complete):' "${output}" ||
    fail "bootstrap continued with a hostile extra environment entry"
}

test_exact_shebang_replay_documents_shell_limit() {
  local version
  # RISK(security): These argv and environment bytes are indistinguishable from
  # kernel shebang execution once Bash has started. A native outer entry is the
  # only way to make this boundary non-spoofable.
  version="$(/usr/bin/env -i SENTINEL_GO_SEALED_ENTRY=direct-v1 \
    /usr/bin/bash --noprofile --norc "${launcher}" --offline version)" ||
    fail "exact shebang replay changed without a native outer entry"
  [[ "${version}" == "go version go1.27.1 linux/amd64" ]] ||
    fail "unexpected exact shebang replay result: ${version}"
}

test_partial_install_is_rejected() {
  local root output
  root="$(new_fixture partial-install)"
  make_partial_install "${root}"
  output="${temporary_root}/partial-install.out"
  expect_failure "${output}" "${root}/scripts/go.sh" --offline version
  /usr/bin/grep -q 'installed Go tree digest mismatch' "${output}" ||
    fail "partial install failed for the wrong reason"

  output="${temporary_root}/partial-bootstrap.out"
  expect_failure "${output}" "${root}/scripts/bootstrap-go.sh"
  /usr/bin/grep -q 'existing Go install does not match the lock' "${output}" ||
    fail "bootstrap accepted or misclassified a partial install"
}

test_lock_controls_binary_digest() {
  local root output wrong_digest altered_lock
  root="$(new_fixture lock-drift)"
  make_partial_install "${root}"
  wrong_digest="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
  altered_lock="${root}/toolchain.lock.json.altered"
  /usr/bin/sed 's/30969f97169d7f43fe6a085873d75613adc21e30818a8c61d95bd27275df4624/'"${wrong_digest}"'/' \
    "${root}/toolchain.lock.json" >"${altered_lock}"
  /usr/bin/chmod 644 -- "${altered_lock}"
  /usr/bin/mv -- "${altered_lock}" "${root}/toolchain.lock.json"
  output="${temporary_root}/lock-drift.out"
  expect_failure "${output}" "${root}/scripts/go.sh" --offline version
  if ! /usr/bin/grep -q 'pinned Go binary digest mismatch' "${output}"; then
    /usr/bin/sed -n '1,20p' "${output}" >&2
    fail "launcher did not use binarySha256 from toolchain.lock.json"
  fi
}

test_toolchain_parent_symlink_is_rejected() {
  local root output
  root="$(new_fixture parent-symlink)"
  /usr/bin/ln -s -- "${repository_root}/.toolchain" "${root}/.toolchain"
  output="${temporary_root}/parent-symlink.out"
  expect_failure "${output}" "${root}/scripts/go.sh" --offline version
  /usr/bin/grep -q 'unsafe toolchain directory' "${output}" ||
    fail "launcher did not identify the parent symlink"

  output="${temporary_root}/parent-symlink-bootstrap.out"
  expect_failure "${output}" "${root}/scripts/bootstrap-go.sh"
  /usr/bin/grep -q 'unsafe toolchain directory' "${output}" ||
    fail "bootstrap did not identify the parent symlink"
}

test_repository_path_and_permissions_are_rejected() {
  local root alias output
  root="$(new_fixture repository-boundary)"
  alias="${temporary_root}/repository-alias"
  /usr/bin/ln -s -- "${root}" "${alias}"
  output="${temporary_root}/repository-alias.out"
  expect_failure "${output}" "${alias}/scripts/go.sh" --offline version
  /usr/bin/grep -q 'launcher path contains a symbolic link' "${output}" ||
    fail "launcher accepted or misclassified a symbolic-link repository path"

  /usr/bin/chmod 777 -- "${root}"
  output="${temporary_root}/repository-mode.out"
  expect_failure "${output}" "${root}/scripts/go.sh" --offline version
  /usr/bin/grep -q 'unsafe repository directory permissions' "${output}" ||
    fail "launcher accepted or misclassified a writable repository root"
}

test_helper_permissions_are_rejected() {
  local root output
  root="$(new_fixture helper-mode)"
  /usr/bin/chmod 666 -- "${root}/scripts/go-toolchain-lib.sh"
  output="${temporary_root}/helper-mode.out"
  expect_failure "${output}" "${root}/scripts/go.sh" --offline version
  /usr/bin/grep -q 'unsafe toolchain helper permissions' "${output}" ||
    fail "launcher accepted or misclassified a writable helper"
}

test_intermediate_state_boundary_is_rejected() {
  local root external_state output
  root="$(new_fixture state-symlink)"
  /usr/bin/mkdir -m 700 -- "${root}/.toolchain"
  external_state="${temporary_root}/external-state"
  /usr/bin/mkdir -m 700 -- "${external_state}"
  /usr/bin/ln -s -- "${external_state}" "${root}/.toolchain/state"
  output="${temporary_root}/state-symlink.out"
  expect_failure "${output}" "${root}/scripts/go.sh" --offline version
  /usr/bin/grep -q 'unsafe toolchain directory' "${output}" ||
    fail "launcher accepted an intermediate state symlink"

  root="$(new_fixture state-mode)"
  /usr/bin/mkdir -m 700 -- "${root}/.toolchain"
  /usr/bin/mkdir -m 777 -- "${root}/.toolchain/state"
  output="${temporary_root}/state-mode.out"
  expect_failure "${output}" "${root}/scripts/go.sh" --offline version
  /usr/bin/grep -q 'unsafe toolchain directory permissions' "${output}" ||
    fail "launcher accepted a writable state directory"
}

test_installed_tree_lock_and_content_are_enforced() {
  local root output altered_lock tampered_file
  root="$(new_fixture tree-lock)"
  make_hardlinked_install "${root}"
  altered_lock="${root}/toolchain.lock.json.altered"
  /usr/bin/sed 's/52dbc6ddf61a5f7019772085d134d610de7f3206ab5d3960d8b9ec63b3aa9564/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/' \
    "${root}/toolchain.lock.json" >"${altered_lock}"
  /usr/bin/chmod 644 -- "${altered_lock}"
  /usr/bin/mv -- "${altered_lock}" "${root}/toolchain.lock.json"
  output="${temporary_root}/tree-lock.out"
  expect_failure "${output}" "${root}/scripts/go.sh" --offline version
  /usr/bin/grep -q 'installed Go tree digest mismatch' "${output}" ||
    fail "launcher did not use installedTreeSha256 from the lock"

  root="$(new_fixture tree-tamper)"
  make_hardlinked_install "${root}"
  tampered_file="${root}/.toolchain/go-1.27.1/README.md.tampered"
  /usr/bin/printf 'tampered\n' >"${tampered_file}"
  /usr/bin/chmod 644 -- "${tampered_file}"
  /usr/bin/mv -- "${tampered_file}" "${root}/.toolchain/go-1.27.1/README.md"
  output="${temporary_root}/tree-tamper.out"
  expect_failure "${output}" "${root}/scripts/go.sh" --offline version
  /usr/bin/grep -q 'installed Go tree digest mismatch' "${output}" ||
    fail "launcher accepted a non-binary installed-tree change"
}

test_world_writable_toolchain_is_rejected() {
  local root output
  root="$(new_fixture unsafe-mode)"
  make_partial_install "${root}"
  /usr/bin/chmod 777 -- "${root}/.toolchain"
  output="${temporary_root}/unsafe-mode.out"
  expect_failure "${output}" "${root}/scripts/go.sh" --offline version
  /usr/bin/grep -q 'unsafe toolchain directory' "${output}" ||
    fail "launcher accepted a world-writable toolchain directory"
}

test_missing_symlink_and_wrong_binary_are_rejected() {
  local missing root output
  missing="$(new_fixture missing)"
  output="${temporary_root}/missing.out"
  expect_failure "${output}" "${missing}/scripts/go.sh" --offline version

  root="$(new_fixture install-symlink)"
  /usr/bin/mkdir -m 700 -- "${root}/.toolchain"
  /usr/bin/ln -s -- "${repository_root}/.toolchain/go-1.27.1" "${root}/.toolchain/go-1.27.1"
  output="${temporary_root}/install-symlink.out"
  expect_failure "${output}" "${root}/scripts/go.sh" --offline version

  root="$(new_fixture binary-symlink)"
  /usr/bin/mkdir -m 700 -p -- "${root}/.toolchain"
  /usr/bin/mkdir -m 755 -p -- "${root}/.toolchain/go-1.27.1/bin"
  /usr/bin/ln -s -- "${pinned_go}" "${root}/.toolchain/go-1.27.1/bin/go"
  output="${temporary_root}/binary-symlink.out"
  expect_failure "${output}" "${root}/scripts/go.sh" --offline version

  root="$(new_fixture wrong-binary)"
  /usr/bin/mkdir -m 700 -p -- "${root}/.toolchain"
  /usr/bin/mkdir -m 755 -p -- "${root}/.toolchain/go-1.27.1/bin"
  /usr/bin/cp -- /usr/bin/true "${root}/.toolchain/go-1.27.1/bin/go"
  output="${temporary_root}/wrong-binary.out"
  expect_failure "${output}" "${root}/scripts/go.sh" --offline version
}

test_caller_working_directory_is_preserved() {
  local caller before after module
  caller="${temporary_root}/caller workspace"
  /usr/bin/mkdir -m 700 -- "${caller}"
  /usr/bin/printf 'module example.com/caller\n\ngo 1.27.0\n' >"${caller}/go.mod"
  before="$(pwd -P)"
  module="$(cd -- "${caller}" && "${launcher}" --offline env GOMOD)" ||
    fail "launcher failed from whitespace caller directory"
  after="$(pwd -P)"
  [[ "${before}" == "${after}" ]] || fail "test caller working directory changed"
  [[ "${module}" == "${caller}/go.mod" ]] || fail "launcher changed caller cwd meaning: ${module}"
}

test_bootstrap_is_idempotent() {
  local first second expected
  first="$("${bootstrap}")" || fail "first bootstrap idempotency probe failed"
  second="$("${bootstrap}")" || fail "second bootstrap idempotency probe failed"
  expected='Go bootstrap already complete: go1.27.1'
  [[ "${first}" == "${expected}" && "${second}" == "${expected}" ]] ||
    fail "bootstrap did not report the same completed state twice"
}

run_case environment test_version_and_offline_environment
run_case hostile-shell test_hostile_shell_startup_is_sealed
run_case explicit-bash test_explicit_bash_is_rejected_as_unsealed
run_case shell-proof-limit test_exact_shebang_replay_documents_shell_limit
run_case partial-install test_partial_install_is_rejected
run_case lock-drift test_lock_controls_binary_digest
run_case parent-symlink test_toolchain_parent_symlink_is_rejected
run_case repository-boundary test_repository_path_and_permissions_are_rejected
run_case helper-mode test_helper_permissions_are_rejected
run_case state-boundary test_intermediate_state_boundary_is_rejected
run_case unsafe-mode test_world_writable_toolchain_is_rejected
run_case tree-integrity test_installed_tree_lock_and_content_are_enforced
run_case narrow-tamper test_missing_symlink_and_wrong_binary_are_rejected
run_case caller-cwd test_caller_working_directory_is_preserved
run_case bootstrap-idempotent test_bootstrap_is_idempotent

[[ ! -e "${repository_root}/.venv" ]] ||
  fail "launcher created an unrelated Python environment"

/usr/bin/printf 'go launcher test passed: %s\n' "${selected_case}"

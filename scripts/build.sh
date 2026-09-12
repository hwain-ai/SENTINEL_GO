#!/usr/bin/bash
set -euo pipefail

readonly script_path="$(/usr/bin/realpath -e -- "${BASH_SOURCE[0]}")"
readonly scripts_root="$(/usr/bin/dirname -- "${script_path}")"
readonly repository_root="$(/usr/bin/realpath -e -- "${scripts_root}/..")"
readonly lock_path="${repository_root}/backend.lock.json"
readonly backend_root="${repository_root}/third_party/mutate4go"
readonly output_root="${repository_root}/.toolchain/bin"

build_error() {
  /usr/bin/printf 'Go build failed: %s\n' "$1" >&2
  exit 1
}

[[ -f "${lock_path}" && ! -L "${lock_path}" ]] || build_error "backend lock is missing or unsafe"
/usr/bin/jq -e '
  type == "object" and
  keys == ["backendName", "bridgeVersion", "coverageSourceSha256", "killPolicy", "operatorSourceSha256", "reportSchemaVersion", "runnerVersion", "schemaVersion", "sourceCommit", "upstreamModuleSha256"] and
  .schemaVersion == "sentinel-go-backend-lock-v1" and
  .backendName == "mutate4go" and
  .sourceCommit == "9016c7adafc1c7e282b5e27768e732e477713af8" and
  .bridgeVersion == "sentinel-mutate4go-bridge/1" and
  .runnerVersion == "sentinel-go-test-runner/1" and
  .reportSchemaVersion == "sentinel-mutate4go-report-v1" and
  .killPolicy == "strict-killed-only-v1" and
  ([.upstreamModuleSha256, .operatorSourceSha256, .coverageSourceSha256] | all(test("^[0-9a-f]{64}$")))
' "${lock_path}" >/dev/null || build_error "backend lock schema is invalid"

verify_locked_file() {
  local relative_path="$1"
  local lock_field="$2"
  local expected actual
  expected="$(/usr/bin/jq -er ".${lock_field}" "${lock_path}")" || build_error "could not read ${lock_field}"
  actual="$(/usr/bin/sha256sum "${backend_root}/${relative_path}" | /usr/bin/cut -d' ' -f1)" || build_error "could not hash ${relative_path}"
  [[ "${actual}" == "${expected}" ]] || build_error "locked backend source mismatch: ${relative_path}"
}

verify_locked_file "go.mod" "upstreamModuleSha256"
verify_locked_file "internal/mutations/mutations.go" "operatorSourceSha256"
verify_locked_file "internal/coverage/coverage.go" "coverageSourceSha256"

if [[ ! -e "${output_root}" ]]; then
  /usr/bin/mkdir -m 700 -- "${output_root}"
fi
[[ -d "${output_root}" && ! -L "${output_root}" && -O "${output_root}" ]] || build_error "output directory is unsafe"
[[ "$(/usr/bin/stat -c '%a' -- "${output_root}")" == "700" ]] || build_error "output directory permissions are unsafe"

backend_temporary="$(/usr/bin/mktemp "${output_root}/.bridge.XXXXXX")"
runner_temporary="$(/usr/bin/mktemp "${output_root}/.runner.XXXXXX")"
sentinel_temporary="$(/usr/bin/mktemp "${output_root}/.sentinel.XXXXXX")"
cleanup_temporaries() {
  [[ ! -e "${backend_temporary}" ]] || /usr/bin/rm -- "${backend_temporary}"
  [[ ! -e "${runner_temporary}" ]] || /usr/bin/rm -- "${runner_temporary}"
  [[ ! -e "${sentinel_temporary}" ]] || /usr/bin/rm -- "${sentinel_temporary}"
}
trap cleanup_temporaries EXIT

"${scripts_root}/go.sh" --offline -C third_party/mutate4go build -trimpath -o "${backend_temporary}" ./cmd/sentinel-mutate4go-bridge
/usr/bin/chmod 700 -- "${backend_temporary}"
[[ "$("${backend_temporary}" --version)" == "sentinel-mutate4go-bridge/1 upstream/9016c7adafc1c7e282b5e27768e732e477713af8" ]] ||
  build_error "built bridge identity is invalid"

"${scripts_root}/go.sh" --offline build -trimpath -o "${runner_temporary}" ./cmd/sentinel-go-test-runner
/usr/bin/chmod 700 -- "${runner_temporary}"
[[ "$("${runner_temporary}" --version)" == "sentinel-go-test-runner/1" ]] ||
  build_error "built typed runner identity is invalid"

"${scripts_root}/go.sh" --offline build -trimpath \
  -ldflags "-X github.com/hwain-hwang/sentinel-go/internal/cli.lockedGoBinary=${repository_root}/.toolchain/go-1.27.1/bin/go" \
  -o "${sentinel_temporary}" ./cmd/sentinel-go
/usr/bin/chmod 700 -- "${sentinel_temporary}"

/usr/bin/mv --force -- "${backend_temporary}" "${output_root}/sentinel-mutate4go-bridge"
/usr/bin/mv --force -- "${runner_temporary}" "${output_root}/sentinel-go-test-runner"
/usr/bin/mv --force -- "${sentinel_temporary}" "${output_root}/sentinel-go"
/usr/bin/printf 'Built %s, %s, and %s\n' "${output_root}/sentinel-go" "${output_root}/sentinel-mutate4go-bridge" "${output_root}/sentinel-go-test-runner"

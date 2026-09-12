#!/usr/bin/bash
set -euo pipefail

repository_root="$(/usr/bin/realpath -e -- "$(/usr/bin/dirname -- "${BASH_SOURCE[0]}")/..")"
cd "${repository_root}"

scripts/build.sh

[[ "$(.toolchain/bin/sentinel-mutate4go-bridge --version)" == \
  "sentinel-mutate4go-bridge/1 upstream/9016c7adafc1c7e282b5e27768e732e477713af8" ]]
[[ "$(.toolchain/bin/sentinel-go-test-runner --version)" == "sentinel-go-test-runner/1" ]]
.toolchain/bin/sentinel-go --help >/dev/null
.toolchain/bin/sentinel-go doctor --backend .toolchain/bin/sentinel-mutate4go-bridge --format json |
  /usr/bin/jq -e '.run.terminalStatus == "passed" and .doctor.pass == true' >/dev/null

/usr/bin/printf 'Go build test passed\n'

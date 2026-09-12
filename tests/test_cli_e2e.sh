#!/usr/bin/bash
set -euo pipefail

repository_root="$(/usr/bin/realpath -e -- "$(/usr/bin/dirname -- "${BASH_SOURCE[0]}")/..")"
sentinel="${repository_root}/.toolchain/bin/sentinel-go"
project="$(/usr/bin/mktemp -d)"
cleanup() {
  /usr/bin/rm -r -- "${project}"
}
trap cleanup EXIT

/usr/bin/chmod 700 -- "${project}"
/usr/bin/printf 'module example.test/e2e\n\ngo 1.27.0\n' >"${project}/go.mod"
/usr/bin/printf 'package e2e\n\nfunc Positive(value int) bool { return value > 0 }\n' >"${project}/value.go"
/usr/bin/printf '%s\n' \
  'package e2e' \
  '' \
  'import "testing"' \
  '' \
  'func TestPositive(t *testing.T) {' \
  '  if Positive(0) || !Positive(1) { t.Fatal("wrong result") }' \
  '}' >"${project}/value_test.go"
/usr/bin/chmod 600 -- "${project}/go.mod" "${project}/value.go" "${project}/value_test.go"

source_before="$(/usr/bin/sha256sum "${project}/value.go" | /usr/bin/cut -d' ' -f1)"
mutation_json="$("${sentinel}" mutation --project "${project}" --source value.go --format json)"
/usr/bin/jq -e '
  .run.terminalStatus == "passed" and
  .mutation.gate.pass == true and
  .mutation.gate.inScope == 2 and
  .mutation.gate.counts.killed == 2
' <<<"${mutation_json}" >/dev/null
source_after="$(/usr/bin/sha256sum "${project}/value.go" | /usr/bin/cut -d' ' -f1)"
[[ "${source_before}" == "${source_after}" ]]

check_json="$("${sentinel}" check --project "${project}" --source value.go --format json)"
/usr/bin/jq -e '
  .run.terminalStatus == "passed" and
  .crap.pass == true and
  .mutation.gate.pass == true
' <<<"${check_json}" >/dev/null

history_json="$("${sentinel}" history --project "${project}" --format json)"
/usr/bin/jq -e '.history.runs | length == 2' <<<"${history_json}" >/dev/null

/usr/bin/printf 'Go CLI end-to-end test passed\n'


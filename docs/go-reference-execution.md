# Go reference execution companion

## Status and purpose

`sentinel-go-reference-runner` is a private, reference-only companion for
comparing fresh executions. It exposes identities already produced by the
typed runner's opt-in replay mode. It is not an admission result, a quality
certificate, a plugin interface, or a replacement for the existing v1 runner.

The companion always calls the existing runner with `CaptureReplay: true`.
That opt-in can change classification. For example, an assertion performed
through an unsupported method value can remain `assertionFailure` in the old
typed invocation while this profile observes `toolError` with `replay: null`.
The companion does not retry with replay capture disabled and does not borrow
an identity from another execution.

## Request and receipt

The Go API is `referenceexecution.Execute(context.Context, Request)`. A request
must supply all four values:

| Value | Contract |
|---|---|
| `ProjectRoot` | Nonempty path to a trusted disposable execution tree |
| `GoBinary` | Nonempty path; no default is added |
| `RequestID` | Exactly 32 lowercase hexadecimal characters |
| `Timeout` | Exact whole milliseconds from 1 through 86,400,000 |

Relative paths retain the existing workspace and runner resolution behavior.
Lexical and numeric checks occur before project filesystem access or child
execution. A nil or already cancelled context returns no receipt.

The JSON receipt contains exactly these fields:

| Field | Meaning |
|---|---|
| `schemaVersion` | `sentinel-go-reference-execution-v1` |
| `profile` | `replay-identity-v1` |
| `requestId` | Caller correlation value, not execution identity |
| `timeoutMilliseconds` | Requested runner allowance |
| `runnerSchema` | Existing `runner.RunnerSchema` value |
| `nonce` | Fresh nonce from this runner invocation |
| `status` | Status observed by this opt-in invocation |
| `inventoryCount` | Static supported-test inventory size |
| `inputSha256` | Guarded project content identity before instrumentation |
| `replay` | Replay identities, or JSON `null` when unavailable |
| `referenceOnly` | Always `true` |
| `certified` | Always `false` |

When `replay` is present, it contains exactly `inventorySha256`,
`resultsSha256`, and `failureSha256`. `failureSha256` is the empty string for a
non-assertion status. A `toolError` receipt with `replay: null` is a valid
private observation, but it is not complete test proof.

The receipt copies scalar counts and digest strings only. It does not expose
the raw inventory, event stream, source paths, test names, or test output.

## Hash meanings

The hashes answer different questions and must not be substituted for one
another:

| Hash | Static or dynamic | What it binds |
|---|---|---|
| `inputSha256` | Static input | The workspace content digest over included relative paths and each file's content digest, captured before instrumentation |
| `inventorySha256` | Static discovery | The existing runner's supported test inventory identity |
| `resultsSha256` | Dynamic execution | The existing runner's sorted official per-test result events |
| `failureSha256` | Dynamic failure | Identified assertion failures and sites for an assertion-failure observation |

`inputSha256` is the existing `workspace.ContentSHA256` algorithm used for the
go-mutesting content identity. It is not an outer artifact content-root or
manifest hash. Replay digests are copied from the existing
`runner.ReplayIdentity`; this wrapper defines no second digest algorithm.

## Guard, timeout, and authority limits

Before execution, `workspace.Create` captures the supplied disposable tree and
creates a spare copy. The spare is only a guard resource. The existing runner
executes the exact supplied tree, then the wrapper verifies that tree against
the pre-execution manifest and removes the guard. Verification and removal run
after runner failure or cancellation. Input-change and cleanup categories are
reported together with execution or cancellation categories, using fixed
diagnostics that do not include underlying paths, filesystem messages, or test
output. Any error returns a zero receipt.

The supplied timeout limits the existing runner execution. Snapshot creation,
the initial filesystem walk, final verification, and guard removal do
not have a context-aware or absolute wall-clock bound in the current workspace
API. Therefore this wrapper is not an absolute-duration boundary.

Workspace ownership checks reduce cross-user interference, but a process with
the same UID can still read or modify the guarded tree and temporary resources.
The nonce distinguishes fresh invocations and the request ID correlates a
caller request; neither authenticates an issuer. No signing key or evidence
consumer is added here.

## Command line

The command requires exactly one occurrence of every flag and accepts separate
or equals forms:

```text
sentinel-go-reference-runner \
  --project-root ./disposable-project \
  --go-binary /trusted/go \
  --request-id 0123456789abcdef0123456789abcdef \
  --timeout-ms 20000
```

Unknown or duplicate flags, positional arguments, missing or empty values,
signed or non-integer milliseconds, invalid IDs, and values outside the range
return exit 3 with `referenceRunnerUsageError`. A sole `--version` prints
`sentinel-go-reference-runner/1` without project access or child execution.

A completed observation returns exit 0 even when its typed status is
`assertionFailure`, `runtimeError`, `compileError`, `timedOut`, or `toolError`.
Execution and cancellation return exit 6 with `referenceRunnerError`. Output
failure returns exit 6 with `referenceRunnerReportError`. Diagnostics never
include the raw underlying error. A reader must require one complete newline-
terminated JSON record and reject partial output.

## Version boundary

This profile does not retrospectively upgrade old v4 or v5 evidence and does
not change the public typed-v1 JSON, bridge, default producer, lock files, build
artifacts, or existing CLI. A later capture algorithm must use a new private
profile instead of silently replacing this one. Bridge correlation, fresh
real-project replay, and strict evidence authority remain separately reviewed
future work.

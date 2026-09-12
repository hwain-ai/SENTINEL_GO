# Private mutate4go reference profile

The existing bridge can explicitly capture fresh reference executions through
the E1 companion described in [Go reference execution](go-reference-execution.md).
This profile reports observations, including unsuccessful or unexecuted stages.
It is reference-only and never a certificate or an admission decision.

## Selection and compatibility

Add exactly one occurrence of each of these optional flags:

```text
--reference-runner-binary /trusted/development/sentinel-go-reference-runner
--reference-run-id 0123456789abcdef0123456789abcdef
```

Separate and equals forms are accepted. Both values must be nonempty; the run
ID is exactly 32 lowercase hexadecimal characters. Missing halves, duplicates,
and invalid IDs are usage errors before filesystem or process access. The
executable receives the same canonical executable checks as existing binaries.
`--runner-binary` remains required for the legacy coverage invocation.

Omitting both flags preserves `sentinel-mutate4go-report-v1`, its original
discovery, classification, arguments, diagnostics, restoration, duration meaning,
and encoder. The sole `--version` behavior is unchanged. New report consumers
must explicitly select this profile; old consumers never receive it by default.

## Execution sequence

1. Read the module path and run one legacy typed coverage invocation. Read its
   coverage file once, bounded to 16MiB, and parse and SHA256-hash those same bytes.
2. Run two actual E1 controls with the normal `--timeout-ms` allowance. Both must
   pass with non-null replay identities and equal input, inventory count,
   inventory digest, and results digest.
3. Reuse upstream discovery and its full candidate order, IDs, and source
   descriptors. Retain uncovered candidates with no compile or replay execution.
4. For each covered candidate, apply once, compile once, and run two E1 replays
   if compilation passes. Each compile and replay uses `--mutant-timeout-ms`
   when selected, otherwise `--timeout-ms`. Restore original source bytes before
   moving to the next candidate, including after execution failure or cancellation.
5. Check complete source/candidate/outcome/stage correspondence and encode the
   separate report. Check parent cancellation after cleanup and encoding before
   writing output.

An E1 invocation receives only `--project-root`, `--go-binary`, `--request-id`,
and `--timeout-ms`. The bridge allocates a fresh cryptographic 16-byte request
ID, records phase/candidate/ordinal/timeout in per-run capture, and requires the
receipt to echo its request ID and timeout. Duplicate request IDs and duplicate
runner nonces are rejected within their respective domains across the run.
Those domains need not be disjoint. Coverage is a legacy producer observation;
it is not an E1 receipt and is assigned no invented nonce.

The same parent context and existing process-group supervision apply. E1 has
the typed-runner timeout plus two seconds of report allowance and three seconds
of cancellation grace. Compile supervision uses the existing compile command
and cleanup mechanism; it does not run a second compile to collect status.
These allowances are not an absolute bound on the entire run or filesystem work.

## Closed report contract

The top-level object has exactly 13 fields:

| Field | Value or meaning |
|---|---|
| `schemaVersion` | `sentinel-mutate4go-reference-report-v1` |
| `profile` | `replay-identity-v1` |
| `runId` | Caller-supplied 32-character lowercase hex correlation value |
| `backendName` | `mutate4go` |
| `backendCommit` | Existing pinned bridge upstream commit |
| `sourceInventory` | Existing path, SHA256, and candidate-count descriptors |
| `candidates` | Existing ID, sourceFile, line, column, and operator descriptors |
| `outcomes` | Existing candidateId, status, and durationNanos descriptors |
| `coverage` | Exactly producerSchema, status, profileSha256 |
| `controls` | Exactly two actual, unmodified E1 receipt objects; index + 1 is ordinal |
| `candidateExecutions` | One stage record for every candidate in discovery order |
| `referenceOnly` | `true` |
| `certified` | `false` |

There is no `completed` field. Arrays remain arrays, including empty arrays.
Coverage has producerSchema `sentinel-go-typed-runner-v1`, status `passed`, and
the digest of the observed coverage file. It does not claim an outer approved
source or artifact hash.

Each candidate execution contains exactly `candidateId`, `compile`, `replays`.
Compile contains exactly `state`, `reason`, `observation`. Replays contains
exactly two rows, each with `ordinal` (1 or 2), `state`, `reason`, `receipt`.

| Branch | Compile | Both replay rows | Outcome |
|---|---|---|---|
| Uncovered | notExecuted, reason uncovered, observation null | notExecuted, reason uncovered, receipt null | uncovered, duration 0 |
| Compile nonzero exit | executed, empty reason, failed observation | notExecuted, reason compileBlocked, receipt null | compileError |
| Compile supervisor deadline | executed, empty reason, timedOut observation | notExecuted, reason compileBlocked, receipt null | timedOut |
| Compile passed | executed, empty reason, passed observation | executed, empty reason, actual E1 receipt | Pair verdict below |

An executed compile observation contains exactly `status` (`passed`, `failed`,
or `timedOut`), `started:true`, actual integer `exitCode` (-1 for a signal or
no normal exit reported by Go), `deadlineExceeded`, and `cleanupSucceeded:true`.
Unknown, contradictory, or incomplete stage branches prevent report emission.

E1 receipts retain exactly its 12 documented fields and nullable three-field
`replay`. The consumer bounds child stdout to 16KiB and rejects missing,
duplicate, unknown, incorrectly typed, nested, truncated, or trailing values.
Integer tokens must be unsigned decimal integers without exponent or fractional
forms. Timeout is 1 through 86,400,000; inventory count is positive and fits Go
int. IDs are lowercase hex32 and digests lowercase hex64. Only toolError may
have replay null; toolError with a non-null replay is also retained. Assertion
failure requires a nonempty hex64 failure digest; every other status requires
an exactly empty string. Fixed schemas, profile, and explicit true/false flags
are mandatory, including the `certified:false` and `replay:null` keys.

## Pair verdict and identity limits

Both candidate replay inventories and counts must equal the passing controls.
The candidate pair must agree on input identity, status, results digest, and
failure digest. Candidate input and dynamic result/failure identities may
differ from controls. Missing replay identity or disagreement yields candidate
`toolError` while preserving both receipt objects unchanged.

| Consistent replay status | Candidate outcome |
|---|---|
| passed | survived |
| assertionFailure | killed |
| compileError, runtimeError, timedOut, toolError | Same status |

A first typed replay failure does not suppress replay 2. A valid toolError/null
receipt is retained and replay 2 is attempted unless the parent is cancelled.
Timeouts and errors are never promoted to killed.

The E1 `inputSha256` is the existing guarded project content identity, and
`inventorySha256` describes static supported-test discovery. `resultsSha256`
describes actual per-test result events, while `failureSha256` describes captured
assertion failures and sites. The bridge copies these existing identities; it
defines no replacement E1 hashing algorithm. Static inventory equality does not
prove complete dynamic test coverage. Timeout result hashes can be partial.

Opting in can change classification: unsupported failure method values can be
legacy assertionFailure but reference toolError with replay null. There is no
retry with capture disabled. Caller IDs, request IDs and runner nonces provide
correlation and freshness checks, not issuer authentication. A process with the
same UID can interfere with the executable, tree or evidence resources.

## Failure and transport limits

Usage errors return 3. Coverage failure or valid but missing/mismatched passing
control evidence returns baseline 4. Existing module/dependency errors remain 5.
Malformed receipts, correlation failures, duplicate nonces, oversized stdout,
child start/nonzero exit/supervisor failures, restoration/cleanup failures and
parent cancellation abort the whole run with execution 6 and no complete report.
The bridge uses fixed diagnostics without raw stderr, error messages, absolute
paths, test names or test output. Candidate source descriptors remain the
existing relative source descriptors required by the report.

The encoded report is retained through a bounded writer with a 16MiB maximum,
including its newline. This bounds retained/emitted report bytes, not total
heap, discovery memory, or encoding/json's internal allocations. Output and
short-write errors return 6 with `bridgeReportEncodeError`. A failed output
write can leave a physical prefix. Consumers must require exit 0 plus one full,
strict JSON record. Exit 0 means a complete observation, even if candidates
were killed, timed out, failed compilation, or produced tool errors.

## Delivery boundary and lookahead

This source change does not complete artifact transport, installation, OCI
execution, admission, or host plugins. In particular the current installed
64KiB stream limit is not proof that it can transport this new report. Existing
v4/v5 evidence is not upgraded retrospectively.

The next gates are review of this bridge profile, separate E3 versioned artifact
and output support, reproducible installation, then fresh authorized project/OCI
observations with matching inventory profiles. A future identity algorithm must
introduce a new private profile. Failed captures must not be completed by legacy
fallback or partial inventories; a later failed-attempt record is separate.
Versioned artifacts permit rollback while preserving earlier evidence history.

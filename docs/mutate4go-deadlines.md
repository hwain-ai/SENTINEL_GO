# Optional mutate4go command deadlines

The native `sentinel-go mutation`, `check`, and `all` commands accept
`--mutant-timeout-ms N`. `N` must be an integer from 1 through 600000. For example:

```sh
sentinel-go mutation --project /path/to/go-module --mutant-timeout-ms 5000
```

This gives each covered candidate's compile check up to 5000 milliseconds.
Each of its two typed test replays separately receives 5000 milliseconds.
The three commands do not share one 5000-millisecond allowance. Compilation
needs enough time for the selected project and available build cache.

Coverage collection and the two original-source controls keep their existing
command limit. The native CLI's overall backend limit remains 10 minutes,
including all candidates. A selected command deadline cannot extend this
outer limit. Existing finite report and cleanup grace periods remain in place;
the option is not a promise of an exact wall-clock return time.

Omitting the option leaves `MutantTimeout` at zero, keeps the legacy command
budget, and emits no new bridge flag. This preserves the old invocation of
pinned bridge artifacts. Explicit zero, negative, fractional, overflowing,
missing, duplicate, and wrong-command CLI values are usage errors before
project state or backend access. The native CLI uses the separate argument
form shown above.

The native orchestrator and adapter also accept `MutantTimeout` as a Go
`time.Duration`. Zero means omission. A positive duration must be an exact
integer number of milliseconds, at most `Timeout` and at most 24 hours.
Sub-millisecond and fractional-millisecond values are rejected, not rounded.
The bridge's optional `--mutant-timeout-ms` has the same relation to its
existing `--timeout-ms`, with an explicit range of 1 through 86400000.
Its Go flag parser accepts separate and equals forms and rejects duplicate
uses of this new flag. Existing options retain their previous behavior.

A candidate command timeout remains `timedOut`, never `killed`. When both
typed replays agree on timeout, later candidates can still run and the full
candidate inventory stays in the report. Compile errors, runtime errors,
process failures, and replay disagreements keep their existing fail-closed
classification. Parent cancellation or overall backend expiry discards the
report, stops later work, and performs the existing restoration and snapshot
cleanup. A timeout does not count as a successful mutation kill or a passing
quality gate.

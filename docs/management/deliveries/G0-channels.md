# G0 selectable-channel delivery — 2026-10-20

G0's management business sources remain frozen. The accepted closing state is the dispatcher's v3 frozen snapshot, recorded in the final section below; concurrent usage-page development is separate. No commit, push, deployment, real credential/configuration read or real model invocation was performed. Existing concurrent changes were preserved.

## Wire and behavior

- Exact wire fields and examples: `../verification-channels.md`; OpenAPI: `../openapi.json`.
- Import/test/wrapped credentials replacement accept channels; new HTTP default is Build, Playground remains explicit, both normalize to Build then Playground before the idempotency digest. Null/empty/duplicate/unknown selections are rejected before admission. Previously admitted private task JSON without channels keeps Playground and its stored digest is untouched.
- Capabilities expose verificationChannels `[build,playground]`, defaultVerificationChannels `[build]`, multiChannelVerification true.
- Single-channel legacy transport fields remain. Both channels return per-channel requested/actual channel, request ID, upstream status, stage, exact index/model/versions and safe error. Aggregate transport identity is not fabricated.
- Each child is persisted before the next session. Later failure, cancellation, task finalization error or restart preserves child observations, makes aggregate success false and does not replay a generation. Store schema remains v2: no new DDL/migration or production migration was executed.
- Verified replacement passes only the exact private verified/refreshed candidate to the coordinator's captured dual CAS. On a published commit whose account identity/index/versions match its original durable upload receipt, top-level and every child's versions are projected together to that committed receipt, preserving prior successful-replacement behavior. Failed verification/CAS keeps captured versions. A current account read never relabels historical evidence.
- Real isolated app adapter fixes one account and one channel per session. Build uses ProxyUnaryCall GET `/v1beta/models`, then ProxyStreamedCall or ProxyUnaryCall generation with proof field 3. Playground uses ListModels/GenerateContent with proof field 5. Guards reject alternate entry points, other accounts/models, repeated generation and continuation. PinnedChannel zero leaves ordinary client routing/catalogue behavior unchanged. No model/network operation runs in a database transaction.

## Files

- Contract/HTTP schema: `internal/managementcontract/{verification,accounts,http}.go`, new channel tests, `docs/management/openapi.json`.
- Admission/task orchestration/checkpoint/recovery: `internal/management/{http,tasks,orchestration}.go`, related tests, `internal/managementstore/tasks.go` and restart checkpoint test.
- Verifier: `internal/managementverify/verifier.go` and tests.
- Protocol/app: `internal/aistudio/{client,generate,pinned_channel}.go`, pinned client tests, `internal/app/{management_projection,management_verification}.go` and tests.
- Shared synthetic fixture: `internal/app/management_acceptance_fixture_test.go`. Aliases/counters/events are listed in the wire document. G6's expected `g6-playground-fail`, `g6-cancel-after-build`, `g6-wrongchannel` are present. Existing lifecycle/synthetic-only controls remain.
- Documentation: wire document, management contract and this delivery.

## Executed verification

Normal same-user host execution, GOTMPDIR `C:\Users\lywx2\AppData\Local\Temp\aistudio2api-management-go-tmp`:

| Command | Result |
| --- | --- |
| `go test -p 2 ./internal/aistudio ./internal/managementcontract ./internal/managementstore ./internal/managementverify ./internal/management ./internal/app -count=1 -timeout=120s` | First broad run: contract/store/verifier/app PASS; two new test fixture setup failures (missing Content-Type and QueueLimit), fixed afterwards |
| `go test -p 2 ./internal/aistudio ./internal/management -count=1 -timeout=120s` | PASS: aistudio 7.088s, management 5.057s |
| `go test -p 2 ./internal/managementcontract ./internal/managementstore ./internal/managementverify ./internal/management ./internal/app -count=1 -timeout=120s` | Final after receipt projection/finalization changes: PASS, respectively 3.961s / 5.578s / 2.621s / 4.696s / 8.631s |
| `go vet ./internal/aistudio ./internal/managementcontract ./internal/managementstore ./internal/managementverify ./internal/management ./internal/app` | PASS, exit 0 |
| `go build ./...` | Windows PASS, exit 0 |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...` | Cross-compile PASS, exit 0; no Linux execution |
| `git diff --check` | PASS; Git line-ending notices only |

Coverage includes default/single/both canonical selection, real Build envelope/decoder and real protected transport with synthetic worker/signer, both directions of no fallback, bounded cancellation preserving the first child, version conflict preventing successor generation, private candidate refresh across sessions, replacement both-success/second-failure/post-verification CAS-conflict, final-checkpoint persistence/CAS failure retaining completed children, legacy queued Playground semantics, canonical idempotency digests, malformed selections, interrupted checkpoint recovery with no replay, wrong pinned leases, and unchanged ordinary Build catalogue defaults. Existing verify:false/no-generation tests passed.

An earlier test attempt could not start because the sandbox Temp mapping was missing; normal execution then encountered an Application Control block for the earlier managementverify test binary. No policy change, executable renaming/copying, or bypass was used. Following actual source changes, the later ordinary test commands above executed managementverify successfully; those are the final results.

## Handoff

Initial selectable-channel source fingerprint (superseded by the fixture-only repair below) covers 328 `*.go`, go.mod/go.sum and `docs/management/openapi.json` inputs, excluding `.git` and web/node_modules. Algorithm: SHA256 of UTF-8 ordinal-sorted `relative/path:sha256(file)` rows joined with LF, without a trailing LF. Initial SHA256:

`d977a0f3f8d76218a1ec57bab8fd25671b78ab0caedbdce02ede19a7a2b9838e`

Canonical fixture build remains the existing ordinary command/path, to be rebuilt by the dispatcher for this source:

```powershell
go test -p 2 -c -o C:\Users\lywx2\AppData\Local\Temp\aistudio2api-management-go-tmp\g6-app.test.exe ./internal/app
```

G0 has not rebuilt or executed the opt-in fixture server for this handoff. The old binary/hash must not be presented as selectable-channel evidence. Independent real Go HTTP × Manager/G6/M4 acceptance is pending the dispatcher's fresh fixture build. No real Google/production test or shared MySQL work is included in this delivery.

## Fixture-only release response repair

After the dispatcher froze production sources, M4 reported reproducible Windows 10053/10054 transport errors at `POST /__g6__/release` after cancellation at before-prepare. Read-only investigation confirmed that the actual Python urllib client sets `Connection: close`, while Go's closing-connection path skips the normal pre-response request-body drain. The old release handler did not read/close the body or explicitly flush its acknowledgement. Existing M4 fixture diagnostics contained no panic or handler error; those logs alone do not prove the origin of every OS reset.

The only Go file changed relative to that freeze is `internal/app/management_acceptance_fixture_test.go`:

- Release now drains the bounded control body to EOF and closes it before releasing the observer barrier. A rejected body does not release the barrier.
- The acknowledgement has fixed JSON bytes, accurate Content-Length and Connection: close. It checks write completion and explicitly Flushes through the response controller.
- The opt-in fixture logs `G6_CONTROL release` with fixed stage/status and byte counts, including write/flush failures. It never prints request headers, bodies, keys or raw transport errors.
- Two local regression tests assert body drain/close before release/response, correct framing and flushing, plus 16 real loopback HTTP Connection: close requests after the before-prepare barrier observes cancellation. Transport errors remain hard failures; no exception is suppressed.

Executed normal same-user command:

```powershell
go test -p 2 ./internal/app -run '^TestManagementAcceptanceFixtureRelease' -count=1 -timeout=30s
```

PASS, exit 0, app 8.368s. `git diff --check` for the fixture and this delivery also passed. No production Go/SQL/OpenAPI source changed, no canonical fixture executable was rebuilt by G0, and no system-policy change was made. The process-level M4 scenario must still be rerun with the dispatcher's freshly rebuilt canonical executable; this local regression is not substituted for that acceptance.

Source was frozen again after that fixture repair. Its source SHA256 using the same 328-input algorithm (superseded by DEF-G6-CHANNEL-001 below):

`1865215d1c800db109bc6139837ce96e467557b2dc5a7ddc322db8246872e1df`

Fixture source file SHA256:

`12f0a159a1afb0da7da3c0b0ecb7fc652befa4bcab1f4e180e3f2b5a81739a79`

Dispatcher rebuild target remains `C:\Users\lywx2\AppData\Local\Temp\aistudio2api-management-go-tmp\g6-app.test.exe`. Previous executable hashes cannot establish this repair's M4/G6 acceptance.

## DEF-G6-CHANNEL-001: atomic checkpoint attribution

G6's v2 real HTTP diagnostic showed a running dual-channel test with Build complete, Playground pending, task item index null, account index 1 and child authIndex 1. The strict Manager correctly refused to classify the child without the owning task item's index. The same omission survived restart; the previous ordinary terminal/cancel path only populated Index after executeItem returned.

The minimal Go fix saves `TaskItem.Index` from the trusted captured `VerificationInput.Account.Index` in the same revision/CAS operation as each checkpoint Result. It never infers the owning index from a child result. The callback checks item AccountID and an already-present Index before saving; a mismatch rejects the entire update without changing the revision, old index or result. `changeChecked` adds this error-returning callback while preserving existing `change` callers. No Manager validation, public protocol, SQL schema, model rule or fixture source changed for this defect.

Changed Go files relative to the previous freeze:

- `internal/management/orchestration.go`: atomic trusted index/result checkpoint and consistency checks.
- `internal/management/tasks.go`: checked task-change callback; existing callers retain the original behavior.
- `internal/management/verification_channels_test.go`: real TaskService/verifier Build-complete/Playground-pending readback, cancellation preservation, matching existing index, conflicting existing index rejection without a save.
- `internal/managementstore/verification_channels_test.go`: persist/read pending index and result together; reject changing an existing index; close/reopen SQLite, recover interruption with the same attributed Build child and no claim/replay.

Executed normal same-user verification:

| Command | Result |
| --- | --- |
| Initial targeted checkpoint/replacement tests | Store PASS; the new cancellation test lacked a Principal scope and returned FORBIDDEN; its synthetic Principal was corrected |
| `go test -p 2 ./internal/management ./internal/managementstore -count=1 -timeout=90s` | PASS, management 4.080s / managementstore 3.346s, exit 0 |
| `go vet ./internal/management ./internal/managementstore` | PASS, exit 0 |
| `git diff --check` for the four changed Go files | PASS |

G0 pre-dispatcher freeze: 328-input source SHA256, same algorithm as above:

`7703c76c9011e330ed434b599574114bdcb83458b29975dc37b85b17f4708ed5`

G0 did not compile or execute the shared canonical exe. Dispatcher owns final all-Go/vet and canonical v3 build, then G6 V08/V11 before its full 12/22 matrices and M4's relevant new-hash acceptance. The Go regression pass does not close the external G6 defect until that real combination is rerun. No real model/configuration/credential access, Git commit/push or deployment occurred.

## Final v3 frozen-snapshot evidence and defect closure

The dispatcher reported that its final v3 snapshot passed all Go tests, vet, canonical fixture compilation, Windows build and Linux cross-build, with all 331 frozen inputs unchanged. This 331-input snapshot is the final dispatcher gate; the earlier G0 328-input fingerprints above describe earlier/differently scoped checkpoints and are not a substitute for it.

Final canonical fixture executable SHA256:

`6752A62C86B42474CC609BB90F9928817054CFB620F377A8D61E29F088B5D7F6`

Path: `C:\Users\lywx2\AppData\Local\Temp\aistudio2api-management-go-tmp\g6-app.test.exe`.

G0 read G6's saved v3 summary and individual targeted/full result JSON. All use that exact executable hash:

| Actual G6 acceptance | Result |
| --- | --- |
| Strict real-client V08 pending/cancel and V11 crash/restart | 2/2 PASS, exit 0, 0 failures/errors/skips, 9.064s |
| Complete selectable-channel matrix | 12/12 PASS, exit 0, 0 failures/errors/skips, 2.639s |
| Existing HTTP compatibility matrix | 22/22 PASS, exit 0, 0 failures/errors/skips, 8.143s |
| Source/driver/executable inputs | 17 inputs unchanged before/after; no G6 rebuild or source edit during acceptance |

Evidence: `runtime/management-development/g6/channel-v3-summary.json`, `channel-result-v3-targeted.json`, `channel-result-v3-final.json`, `channel-v3-source-driver-exe-before.json` and `channel-v3-source-driver-exe-after.json`; logs are `channel-http-v3-targeted.log`, `channel-http-v3-final.log` and `python-http-selectable-v3-final.log` in that same directory.

**DEF-G6-CHANNEL-001 is closed on v3.** The strict real Manager/client now recognizes the completed Build child while Playground is pending and after restart, without weakening ownership validation or replaying model work. V10 remains a safe unknown/unlocated-model-ack GET-only result, not successful model-result recovery.

M4's final v3 acceptance is now PASS. G0 read both saved result JSON files; both identify the exact canonical executable SHA256 above:

| Actual M4 acceptance | Result |
| --- | --- |
| Real Go HTTP and Manager components | 29/29 PASS, 83.390s; 0 failures/errors/skips/isolation denials |
| Chinese GoQt integration | 2/2 PASS, 8.531s; 0 failures/errors/skips/isolation denials |

Evidence in the Manager repository:

- `G:/code/gemini30/gemini-manager/runtime/devices/aistudio2api-acceptance/m4-channels-v3-http-final-20261020/go-http/result.json`
- `G:/code/gemini30/gemini-manager/runtime/devices/aistudio2api-acceptance/m4-channels-v3-goqt-final-20261020/go-qt/result.json`

The dispatcher independently compared all 161 source_inputs plus the driver and fixture before/after and reported every input unchanged. These are new-hash actual results, not an inference from earlier passes; the historical failures above remain recorded. Scope is real local Go HTTP/Manager/Qt with synthetic upstream and the SQLite Manager SQL fixture. Shared MySQL remains deferred/not prepared; real models were not called. M4's remaining work is final documentation/static checks, not an unperformed HTTP/GoQt acceptance gate.

The formal working directory also contains the user's separate “优化用量页加载速度” task. Its three API/requestdb drift files and new usage files were preserved. The dispatcher's passing, unchanged 331-input test snapshot establishes this management v3 gate only; it does not establish that the concurrent usage-page work has been finally integrated or validated. G0 did not change those files.

This closing update changed only this G0 delivery Markdown. Business Go/SQL/OpenAPI remains frozen; G0 did not rerun tests, compile the shared executable, commit, push or deploy.

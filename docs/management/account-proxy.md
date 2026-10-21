# Optional account proxy writes

2026-10-21. The management capability `accountProxyWrite:true` advertises optional `proxy` on each import item and on the credential-replacement envelope, outside `credentials`. Omission keeps the existing proxy on replacement and the default account/global exit on import. URLs use the existing account config validator (`http`, `https`, `socks5`); no connectivity or model probe is added.

The raw URL remains private task input and private account configuration. Task/account responses and audit do not expose it. It participates in the canonical admission digest, so changing it with the same idempotency key conflicts. A committed upload receipt includes `proxyApplied:true` only when the requested proxy was included in the durable account commit; it never returns authentication material. Clients must check that original receipt and the current account versions, not infer success from task acceptance.

The existing account mutation lease, coordinator gate and recoverable file participant merge only the proxy field into the latest config, then commit credential/config files and metadata together. Other account settings remain intact. A real proxy change advances `stateVersion`; native console proxy changes use the same coordinator and CAS. Replacement with `proxy` and `verify:false` preserves enabled/disabled state. Archive rejection remains unchanged. Requests without `proxy` retain their previous semantics and canonical private input serialization.

No schema migration is needed: the existing private task input, commit journal and receipt JSON carry the extension. Recovery retains both files and the original proxy receipt after database commit, or rolls both files back before commit. Unknown submissions are reconciled by the original task identity; clients must not resubmit or redraw a proxy.

Update this backend before enabling the Manager random proxy checkbox. Manager blocks that option before browser work if the capability is absent; the default unchecked flow remains compatible with old services. No deployment is part of this implementation.

Validation used synthetic proxies and isolated storage: formal-source `go test -p 2 ./cmd/... ./internal/... -count=1 -timeout=3m` and `go vet ./cmd/... ./internal/...` passed. New cases cover HTTP private admission/idempotency, worker forwarding with zero model calls, disabled replacement, omitted proxy, native CAS conflict, rollback, and real account-file/SQLite crash recovery at applied, metadata, receipt and postcommit boundaries. A root `./...` sweep also discovers a pre-existing `runtime/review-20261020` temporary package whose copied review tests do not compile (`NewHandler` undefined); those files were preserved.

Real proxies, production service/account operations and macOS execution were not tested. Offline tests are not evidence of remote model availability or browser login.

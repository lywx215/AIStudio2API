# Verification channel wire contract (2026-10-20)

Under `/api/manage/v1`, import (`POST /accounts/import`), wrapped replacement (`PUT /accounts/{id}/credentials`) and test (`POST /accounts/{id}/test`) accept optional `channels: ("build"|"playground")[]`. Omitted means `["build"]`. The only canonical selections are `["build"]`, `["playground"]`, `["build","playground"]`. Reverse input order normalizes before the idempotency digest. Empty, null, duplicate and unknown selections are invalid. `verify:false` performs zero verification/model requests regardless of selection. Bare legacy replacement defaults to Build.

Previously admitted private task JSON without channels retains its historical Playground selection when queued work is loaded; its admission digest is not rewritten. New HTTP requests always persist the explicit canonical selection. Running work remains interrupted on restart, with no upstream replay. Historical single Playground results remain readable.

Capabilities add `defaultVerificationChannels:["build"]` and `multiChannelVerification:true`; `verificationChannels` is `["build","playground"]`. Clients must check this before sending Build/both to an older Playground-only backend.

Task item `result` adds `channels` and `channelResults`. Each child has `requestedChannel`, optional `actualChannel`, `success`, `authIndex`, `model`, optional `requestId`, nullable `upstreamStatus`, `stage`, `credentialVersion`, `stateVersion`, and optional safe `error:{code,message}` from the management error catalogue. No observation means actualChannel/requestId are absent and status is null. Failed/cancelled/interrupted children have an error. Do not infer model success from 200, connection_verified, or a configured channel.

Single-channel results retain top-level `actualChannel`, `requestId`, and `upstreamStatus` matching their child. For both channels those first two fields are absent and upstreamStatus is null. Top-level success requires success on every requested channel. Top-level stage is model_verified/connection_verified only on complete success, otherwise failed/cancelled/interrupted or verifying during a durable checkpoint. Completed children survive later failure, cancellation and restart. Restart interrupts running tasks without replaying their upstream calls.

Example dual-channel result (task item carries its accountId):

```json
{
  "success": false, "authIndex": 7, "model": "synthetic-model",
  "upstreamStatus": null, "stage": "failed",
  "credentialVersion": 3, "stateVersion": 5,
  "channels": ["build", "playground"],
  "channelResults": [
    {"requestedChannel":"build","actualChannel":"build","success":true,"authIndex":7,"model":"synthetic-model","requestId":"req_dfcfb928-d90b-4a81-a23e-bc5ed7eecab7","upstreamStatus":200,"stage":"model_verified","credentialVersion":3,"stateVersion":5},
    {"requestedChannel":"playground","actualChannel":"playground","success":false,"authIndex":7,"model":"synthetic-model","requestId":"req_676ad95f-04ae-4ceb-b6ad-614f647772bd","upstreamStatus":403,"stage":"failed","credentialVersion":3,"stateVersion":5,"error":{"code":"VERIFICATION_FAILED","message":"Target account verification failed."}}
  ]
}
```

Every channel uses a separate isolated account-pinned session and at most one generation. Build uses its own catalogue (`ProxyUnaryCall` GET /v1beta/models) and `ProxyStreamedCall`/`ProxyUnaryCall` generation, never Playground. Playground uses ListModels/GenerateContent, never Build. No account fallback, ordinary router, automatic continuation or repeated generation is admitted. All networking runs outside storage transactions. No storage schema migration is needed: channels and evidence use the existing private-input/public-task JSON.


Verified replacement checkpoints retain captured versions before commit. After the coordinator commits the exact verified/refreshed private candidate under the captured dual CAS, and the published commit account identity/index/versions match the original durable upload receipt, the successful result and **every** child are consistently projected onto that receipt's committed versions (preserving the previous successful-replacement behavior). A live account read is never used to relabel evidence. Failed verification or CAS retains captured versions and completed child observations. Failure of task finalization/audit, cancellation or recovery sets aggregate success false without deleting completed children. Candidate cookies stay private.

Synthetic fixture aliases (no network): `g6-ok` succeeds on either requested channel; `g6-buildonly` rejects Playground; `g6-playgroundonly` rejects Build; `g6-playground-fail` succeeds on Build and returns Playground 403; `g6-cancel-after-build` (also `g6-playground-cancel`) completes/checkpoints Build then waits in Playground until cancelled; `g6-playground-timeout` times out only Playground; `g6-wrongchannel` returns the other channel as untrusted evidence. Existing wrong-account/model/empty/terminal scenarios remain. `/__g6__/state` reports generationCalls, buildCalls, playgroundCalls according to the requested execution channel plus safe events `{accountId,channel,model,mode}`. Connection events do not increment generation counters. G6 controls cancellation/restart through the existing loopback-only fixture lifecycle; the production namespace has no probe or barrier.

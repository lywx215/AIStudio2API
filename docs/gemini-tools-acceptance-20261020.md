# ais01 Gemini Search and URL-context acceptance

2026-10-20 (Asia/Shanghai). Target: `https://ais01.zeabur.app`, Zeabur service `6ad6f80ef5d983bf75dce3b5`, environment `690ee9d02655171f1baa3266`, GitHub `lywx215/AIStudio2API` / `main`.

## Initial release

Management implementation commit `94e6af7b25d7695c0e0aacaa7463200ef2147a2c` was pushed successfully. Zeabur automatically deployed it as `6ad70cf67d48a89b2ad3dabf`, status RUNNING. `/health` returned 200; live index and JavaScript entry hashes matched the locally built frontend. The management route returned its new request-ID/no-store unauthorized envelope. These checks establish more than commit equality.

Frontend: 27 tests, lint and build passed; Go vet and Linux amd64 cross-build passed. A release-time Go test rerun passed other packages but Windows Application Control blocked the app test executable. Earlier full-suite and cross-language passes for the same Go inputs remain separately recorded in `management/remediation-20261020.md`; the blocked rerun is not called a pass.

## Authorized real model calls

The user explicitly authorized reading this service's model `PROXY_API_KEY` into process memory and sending it only to the exact `ais01` HTTPS origin, without output, storage or redirects. The Zeabur management credential was never used as a model Key; the separate Aitoapi DPAPI credential was never sent to this service.

Requests use `/v1beta/models/gemini-3.8-flash:generateContent`. Model discovery returned `models/gemini-3.8-flash`. Safe request/response artifacts are under ignored `runtime/deployment-acceptance/20261020/`; credential-bearing headers are excluded and responses are checked against the in-memory Key before storage.

The service's existing runtime logs were matched to each of the four response IDs below. All four explicitly record **Build** as the actual channel. No channel configuration was changed. This is ordinary model-API acceptance; management's separately pinned Playground validator retains its own acceptance scope.

| Case | HTTP / finish | Execution evidence | Duration |
|---|---|---|---|
| Google Search | 200 / STOP | 1 actual `webSearchQueries` entry, 2 grounding chunks, nonempty answer | 6.172s |
| URL context, snake_case request | 200 / STOP | exact requested Google documentation source; 3,636 `toolUsePromptTokenCount`; nonempty answer | 5.469s |
| Search + URL, camelCase request | 200 / STOP | 2 search queries, 3 sources, nonempty answer; this run alone does not independently prove URL execution because tool tokens are 0 | 7.671s |
| Search + fresh RFC URL | 200 / STOP | search query plus RFC 9309 source and **10,694 tool input tokens**; nonempty RFC-specific answer and search result | 12.578s |

The last case uses both tools in one request and provides stronger evidence than an HTTP 200 or the model claiming it searched. No fallback model was used. Before credential-source authorization, an unauthenticated call returned 401; it remains a failed-auth historical record, not a model execution.

## Confirmed compatibility fix

The Build upstream JSON decoder discarded `candidates[].urlContextMetadata`. Synthetic upstream JSON replay through the actual decoder and Gemini HTTP handler reproduced the loss in unary, JSON-array streaming and SSE responses. The fix introduces a separate URL-context event, preserves original URLs/statuses, merges streaming updates without aliasing upstream storage, and emits canonical Gemini `urlContextMetadata.urlMetadata` on the final candidate. Failed/unsafe/paywall/future statuses are retained. URL retrieval attempts can satisfy an explicit required built-in-tool contract; nil or empty metadata cannot.

Regression tests cover all three HTTP forms, repeated metadata-only frames, status updates, failure/future values, completion/usage preservation, storage isolation and the negative rule: prompt URLs, answer text and search grounding must never manufacture URL retrieval success.

The full rerun also exposed a Windows-only fixture defect: a string-prefix path assertion rejected mixed slash styles, and a worker callback's `t.Fatal` prevented the cancellation test from receiving its startup signal. The original run timed out at 608.357s and remains a failure record. The fixture now uses path-component containment, returns callback errors and bounds waits; production verification code was unchanged. G4's 12 top-level verification tests passed in 10.415s using the original forward-slash GOTMPDIR.

Final source-stable checks after both fixes:

- `go test -p 2 ./... -count=1 -timeout=2m`: exit 0, including app 9.900s.
- `go vet -p 2 ./...`: exit 0.
- Linux amd64, CGO-disabled CLI build: exit 0; this is a cross-build, not Linux service execution.
- 325 Go/SQL/module inputs unchanged before/after. Logs/manifests: `url-context-final-*` in the existing C-drive GOTMPDIR.
- Scoped whitespace and credential-pattern scans passed; no temporary protocol-probe code remains.

Sources for the public shape and status semantics: [Google GenerateContent reference](https://ai.google.dev/api/generate-content#UrlContextMetadata), [URL-context guide](https://ai.google.dev/gemini-api/docs/generate-content/url-context).

## Evidence boundary

The original live Build responses contained grounding and usage but no public `urlContextMetadata`. The Build JSON decoder's omission was reproduced and fixed as described above. Native Playground uses a separate private numeric protocol: this change does not guess its field number or fabricate statuses from citations, and does not claim native Playground retrieval-status mapping or live pinned-validator acceptance. Search and URL-context execution through the configured production Build route is demonstrated above.

A proposed fixed, redacted production protocol probe was rejected by automatic approval as an additional debug rollout. It was not pushed; all temporary probe code was removed. Stronger normal tool-execution evidence made that rollout unnecessary. No later authorization response is needed or used for that abandoned probe.

The permanent compatibility fix and final deployed revision are verified separately after release; the ignored final acceptance summary records their exact commit/deployment IDs and real smoke results. No account-management operations, credential rotations or environment-setting changes were performed.

## Request form for reproduction

Use a model Key in the `X-Goog-API-Key` header, never a management Key. POST to `/v1beta/models/gemini-3.8-flash:generateContent` (or `:streamGenerateContent?alt=sse`) with JSON such as:

```json
{
  "contents": [{"role":"user","parts":[{"text":"Read https://www.rfc-editor.org/rfc/rfc9116.html using URL context, then use Google Search to find the latest stable Python release. Cite both sources."}]}],
  "tools": [{"googleSearch":{}},{"urlContext":{}}],
  "generationConfig": {"maxOutputTokens":2048,"temperature":0}
}
```

Require a nonempty visible answer, STOP, the requested model version, real search queries/sources, and explicit URL success metadata or positive URL-tool input usage with the exact requested URL source. HTTP 200 alone and model self-report are insufficient. Missing URL retrieval-status metadata is reported separately from tool-execution evidence.

# JavaScript SDK offline contract

This fixture pins the official [`@google/genai`](https://www.npmjs.com/package/@google/genai/v/2.22.0) package to **2.22.0**. The lockfile pins transitive dependencies and registry integrity hashes. Source and API reference: [googleapis/js-genai](https://github.com/googleapis/js-genai), [official SDK documentation](https://googleapis.github.io/js-genai/).

Install once with scripts disabled:

```powershell
cd scripts/business-addon/sdk-js
npm ci --ignore-scripts --no-audit --no-fund
npm test
```

Or include the fixture in the Business Gemini package tests from the repository root:

```powershell
$env:BUSINESS_GENAI_SDK_NODE = (Get-Command node).Source
go test -mod=readonly ./internal/addons/business/gemini -count=1
```

Node 22.18 or newer is required by this fixture. No user account, API key, model call or network listener is used. A fixed inert key is supplied to the SDK and removed before the Go pipe protocol. The worker replaces global `fetch` before importing the SDK, rejects any origin except `http://business-sdk.invalid`, and blocks alternate Node socket paths. SDK 2.22.0 does not expose the newer `HttpOptions.fetch` option.

The Go fixture mounts the actual Business Gemini and file handlers with a synthetic generator and temporary file store. It does **not** exercise Host/API authentication, account scheduling, real Google transport, or semantic support for optional parameters. Host/API tests remain separate.

Coverage:

- Model list pagination/get and supported action names.
- Unary generation, inline files and mapped values including `0`, `false`, and opaque schema property names.
- Unsupported optional settings accepted and ignored by the handler.
- Thought/signature and suggestions bytes preserved in unary and streaming responses.
- SSE split across JSON tokens and UTF-8 boundaries, normal `STOP`, and server partial-failure/missing-terminal error without a success terminator or retry. The consumer detects missing completion; the pinned SDK itself does not reject these error events.
- Syntactically valid history reaches the synthetic session lookup and gets HTTP409 for an unknown completed prefix; it is not converted to text.
- Invalid parts, MIME and supported parameter values rejected; native `countTokens` remains HTTP501.
- SDK resumable upload, file get/list/reference/delete, a native download probe and foreign-origin reference rejection.

JavaScript numbers cannot exactly represent integers beyond `Number.MAX_SAFE_INTEGER`; this fixture checks the maximum safe integer and does not claim arbitrary-precision serialization. The Python fixture separately covers larger integers. SDK 2.22.0 drops `temperature: null` before sending JSON; this behavior does not alter the handler's explicit-null validation.

All generation counts in this fixture refer to the synthetic generator. Passing it is SDK/handler compatibility evidence, not upstream Google capability verification.

## Phase 5 typed output and Chat continuation

`TestGeminiSDKPhase5PartsAndChat` runs both pinned SDKs against the real handler
through their `--phase5-worker` pipe branches. Each SDK performs 13 requests and
11 synthetic generations. It checks ordered text/code/result/text, signatures,
all three execution outcomes, final grounding metadata with UTF-8 byte indices,
ordinary Chat and streamed Chat continuation, tampered code history and failed
prefix rejection. It inspects every Part rather than relying on `.text` or the
first-code accessors. The endpoint carries the final real Part with STOP; this
allows the Python Chat SDK to retain its completed model turn. Adjacent model
Content fragments produced by the JS Chat SDK are normalized by the parser.

`phase5-live.mjs` is a separate opt-in two-generation acceptance script with the
same required arguments as `live.mjs`. It verifies real Argon Chat stream then
automatic continuation, complete Part retention, nonempty final content, exact
session counters and resource release. It permits only the two fixed local
generation endpoints and two unauthenticated status reads; no redirects or
generation retries. It is never run by the offline suite. Python's corresponding
`../phase5_live.py` also exercises synthetic TXT upload and file cleanup.

## Phase 6 native source metadata

`TestGeminiSDKPhase6RetrievedCitationsAndChat` runs both pinned SDKs through
`--phase6-worker`. Each performs 7 pipe requests and 6 synthetic generations.
The fixture checks retrievedContext URI/title (without invented web/tool
provenance), cross-Part UTF-8 supports, source-only citation URI/license,
metadata only at STOP, mixed typed Parts and exact Chat continuation. On the
wire, the citation field is `citationMetadata.citationSources`; both SDKs
expose it as `.citations`. Missing offsets must remain absent rather than zero.
Candidate metadata never becomes content history.

Both local live scripts now accept `--phase 6` (default remains `--phase 5`).
The phase flag changes synthetic markers and reports; it does not relax the
fixed local origin, two-generation limit, redirect rejection or no-retry rule.
Receipts separately count retrievedContext, web and citation sources. Zero
means those structures were not observed, even if the response text mentions
search or includes a URL. Live scripts remain outside all offline test suites.

## SDK stream error limitation

In SDK 2.22.0, `processStreamResponse` unwraps standard `data:` SSE events into `HttpResponse` values. `generateContentResponseFromMldev` then copies only known response fields and drops `error`. Consequently a valid HTTP200 SSE stream containing a final `{"error":...}` event produces an empty SDK chunk and ends iteration without throwing. Its raw-fetch error check does not reliably cover framed SSE, especially across network chunk boundaries.

The fixture asserts both layers: the real handler sends error code 502 **inside the HTTP200 stream's error envelope**, no `STOP`, and no request replay; the SDK yields the partial text plus an empty converted chunk. It does not count the SDK's normal iterator exit as success. This is a pinned-SDK limitation, not a reason to change server errors into successful results.

For Business streams, `stream-consumer.mjs` provides `consumeBusinessStream(source, onChunk)`. It preserves each SDK chunk for incremental consumption, propagates SDK/transport exceptions, recognizes any retained error field, and requires a successful `STOP` before completion. It stores no output buffer and never repeats a request:

```javascript
await consumeBusinessStream(client.models.generateContentStream({ model, contents }),
  chunk => process.stdout.write(chunk.text ?? ''));
```

This helper is an explicit consumer check, not a modification of the official SDK or a claim that its native iterator rejects every stream failure.

## Explicit local acceptance

`live.mjs` is a separate opt-in script. It requires `--allow-live --session PATH --report PATH`; without all three it exits before reading credentials. It reads the existing local model API key into memory and permits exactly one `POST` to `http://127.0.0.1:2048/v1beta/models/business-gemini-4-argon:generateContent`, with redirect rejection and SDK retries disabled. It records only the synthetic response checks, timings and safe error classifications. It is not invoked by `npm test` and must be run only for an authorized existing-account acceptance task.

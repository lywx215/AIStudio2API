// The official SDK runs against real Go handlers through a socket-free pipe.
// This is a serialization/handler contract fixture, not Google live evidence.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import net from 'node:net';
import http from 'node:http';
import https from 'node:https';
import { createInterface } from 'node:readline';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { consumeBusinessStream } from './stream-consumer.mjs';

const version = '2.22.0';
const origin = 'http://business-sdk.invalid';
const model = 'business-test';
const provider = { 'X-AIStudio-Provider': 'business' };
const root = fileURLToPath(new URL('../../../', import.meta.url));
assert.equal(JSON.parse(fs.readFileSync(new URL('./node_modules/@google/genai/package.json', import.meta.url))).version, version);

if (!process.argv.includes('--bridge-worker')) {
  const result = spawnSync('go', ['test', '-mod=readonly', './internal/addons/business/gemini',
    '-run', process.argv.includes('--completion-worker') ? '^TestGeminiSDKCompletionMediaFilesAndCatalog/JavaScript$' :
      process.argv.includes('--phase6-worker') ? '^TestGeminiSDKPhase6RetrievedCitationsAndChat/JavaScript$' :
      process.argv.includes('--phase5-worker') ? '^TestGeminiSDKPhase5PartsAndChat/JavaScript$' : '^TestGeminiJavaScriptSDKOfflineHarness$', '-count=1', '-v'], {
    cwd: root, env: { ...process.env, BUSINESS_GENAI_SDK_NODE: process.execPath }, stdio: 'inherit',
  });
  process.exit(result.status ?? 1);
}
assert.equal(process.env.BUSINESS_SDK_PIPE, '1', 'Worker requires the Go pipe fixture');

// This SDK release calls global fetch (HttpOptions.fetch is not in 2.22.0).
// Override it before importing the SDK and reject all alternate socket paths.
const noNetwork = () => { throw new Error('Network sockets are forbidden in the SDK fixture'); };
net.connect = noNetwork;
net.createConnection = noNetwork;
net.Socket.prototype.connect = noNetwork;
http.request = noNetwork;
http.get = noNetwork;
https.request = noNetwork;
https.get = noNetwork;
const lines = createInterface({ input: process.stdin, crlfDelay: Infinity });
const replies = lines[Symbol.asyncIterator]();
let requests = 0;
let generations = 0;
let inFlight = false;
let lastWireStream;
const coverage = new Set();

async function pipeFetch(input, init) {
  assert.equal(inFlight, false, 'Fixture deliberately serializes requests');
  const request = new Request(input, init);
  const url = new URL(request.url);
  assert.equal(url.origin, origin, 'Request escaped the in-memory fixture origin');
  assert.equal(url.username, '');
  assert.equal(url.password, '');
  const bytes = Buffer.from(await request.arrayBuffer());
  assert.ok(bytes.length <= 2 ** 20, 'Fixture request exceeded its bound');
  const headers = Object.fromEntries([...request.headers].filter(([name]) =>
    !['authorization', 'x-goog-api-key'].includes(name.toLowerCase())));
  inFlight = true;
  process.stdout.write(JSON.stringify({ method: request.method, url: request.url, headers, body: bytes.toString('base64') }) + '\n');
  const reply = await replies.next();
  inFlight = false;
  assert.equal(reply.done, false, 'Go closed its pipe before responding');
  const response = JSON.parse(reply.value);
  requests += 1;
  generations = Number(response.headers['X-Fixture-Generations']);
  const data = Buffer.from(response.body, 'base64');
  const isSSE = new Headers(response.headers).get('content-type')?.startsWith('text/event-stream');
  const wireEvents = isSSE ? data.toString('utf8').split(/\r?\n/)
    .filter(line => line.startsWith('data: ')).map(line => JSON.parse(line.slice(6))) : [];
  lastWireStream = { errors: wireEvents.filter(event => event.error).map(event => event.error.code),
    stops: wireEvents.filter(event => event.candidates?.some(candidate => candidate.finishReason === 'STOP')).length };
  // Deliberately split JSON tokens and multibyte UTF-8 across fetch chunks.
  let offset = 0;
  const body = new ReadableStream({
    pull(controller) {
      if (offset >= data.length) return controller.close();
      controller.enqueue(data.subarray(offset, offset += 7));
    },
  });
  return new Response([204, 205, 304].includes(response.status) ? null : body,
    { status: response.status, headers: response.headers });
}
globalThis.fetch = pipeFetch;
const { GoogleGenAI, createPartFromUri } = await import('@google/genai');
const client = new GoogleGenAI({ apiKey: 'offline-inert-fixture', vertexai: false,
  httpOptions: { baseUrl: origin, apiVersion: 'v1beta', headers: provider, retryOptions: { attempts: 1 } } });

async function generate(contents = 'hello', config) {
  const response = await client.models.generateContent({ model, contents, config });
  assert.equal(response.text, 'fixture-输出');
  assert.equal(response.candidates[0].finishReason, 'STOP');
  assert.equal(response.modelVersion, undefined);
  assert.equal(response.usageMetadata, undefined);
  return response;
}
async function rejected(contents, status = 400, config) {
  const before = generations;
  await assert.rejects(client.models.generateContent({ model, contents, config }), error => error.status === status);
  assert.equal(generations, before, 'Invalid request reached the generator');
}
async function stream(contents) {
  const result = [];
  for await (const chunk of await client.models.generateContentStream({ model, contents })) result.push(chunk);
  return result;
}

async function phase5Worker() {
  const expectedParts = outcome => [
    { text: '雨A' },
    { executableCode: { language: 'PYTHON', code: 'print(21 * 2)\n' }, thoughtSignature: '+/8=' },
    { codeExecutionResult: { outcome, output: '42\n' } },
    { text: 'fin' },
  ];
  const metadata = {
    groundingChunks: [{ web: { uri: 'https://example.invalid/source', title: 'Synthetic source' } }],
    groundingSupports: [{ segment: { partIndex: 0, startIndex: 0, endIndex: 3, text: '雨' }, groundingChunkIndices: [0] }],
  };
  const user = text => ({ role: 'user', parts: [{ text }] });
  const checkResponse = (response, outcome = 'OUTCOME_OK') => {
    const candidate = response.candidates[0];
    assert.deepEqual(candidate.content.parts, expectedParts(outcome));
    assert.deepEqual(candidate.groundingMetadata, metadata);
    assert.equal(candidate.finishReason, 'STOP');
    assert.equal(response.usageMetadata, undefined);
    assert.equal(response.modelVersion, undefined);
  };
  const checkChunks = chunks => {
    assert.equal(chunks.length, 4, 'Each real part must occur once; no invented empty terminal');
    assert.deepEqual(chunks.flatMap(chunk => chunk.candidates?.[0].content?.parts ?? []), expectedParts('OUTCOME_OK'));
    assert.equal(chunks.at(-1).candidates[0].finishReason, 'STOP');
    assert.deepEqual(chunks.at(-1).candidates[0].content.parts, [{ text: 'fin' }]);
    assert.deepEqual(chunks.at(-1).candidates[0].groundingMetadata, metadata);
    assert.ok(chunks.slice(0, -1).every(chunk => chunk.candidates[0].groundingMetadata === undefined));
    assert.deepEqual(lastWireStream, { errors: [], stops: 1 });
  };
  const followup = async contents => {
    const before = generations;
    const response = await client.models.generateContent({ model, contents });
    assert.equal(response.text, 'phase5-followup-ok');
    assert.equal(response.candidates[0].finishReason, 'STOP');
    assert.equal(generations, before + 1);
  };

  const prompt = 'phase5-unary';
  const response = await client.models.generateContent({ model, contents: prompt });
  checkResponse(response);
  await followup([user(prompt), response.candidates[0].content, user('phase5-followup')]);
  const altered = structuredClone(response.candidates[0].content);
  altered.parts[1].executableCode.code = 'print(0)\n';
  await rejected([user(prompt), altered, user('phase5-followup')], 409);
  coverage.add('native-code-result-signature-and-exact-history');

  for (const outcome of ['OUTCOME_FAILED', 'OUTCOME_DEADLINE_EXCEEDED']) {
    checkResponse(await client.models.generateContent({ model, contents: 'phase5-outcome-' + outcome }), outcome);
  }
  coverage.add('execution-outcomes-are-successful-parts');
  const chunks = await stream('phase5-stream');
  checkChunks(chunks);
  assert.deepEqual(await consumeBusinessStream(chunks), { events: 4, completed: true });
  await followup([user('phase5-stream'), { role: 'model', parts: chunks.flatMap(chunk => chunk.candidates[0].content.parts) }, user('phase5-followup')]);
  coverage.add('grounding-only-terminal-utf8-byte-indices');

  const chat = client.chats.create({ model });
  checkResponse(await chat.sendMessage({ message: 'phase5-chat-unary' }));
  assert.equal((await chat.sendMessage({ message: 'phase5-followup' })).text, 'phase5-followup-ok');
  const streamingChat = client.chats.create({ model });
  const chatChunks = [];
  for await (const chunk of await streamingChat.sendMessageStream({ message: 'phase5-chat-stream' })) chatChunks.push(chunk);
  checkChunks(chatChunks);
  const history = streamingChat.getHistory(true);
  assert.equal(history[0].role, 'user');
  assert.ok(history.slice(1).length > 1, 'Pinned JS Chat consecutive model contents were not exercised');
  assert.ok(history.slice(1).every(content => content.role === 'model'));
  assert.deepEqual(history.slice(1).flatMap(content => content.parts), expectedParts('OUTCOME_OK'));
  assert.equal((await streamingChat.sendMessage({ message: 'phase5-followup' })).text, 'phase5-followup-ok');
  coverage.add('native-chat-unary-stream-next-turn');

  const failedChat = client.chats.create({ model });
  const partial = [];
  let before = generations;
  for await (const chunk of await failedChat.sendMessageStream({ message: 'phase5-fail' })) partial.push(chunk);
  assert.equal(generations, before + 1, 'Failed stream was retried');
  assert.deepEqual(lastWireStream, { errors: [502], stops: 0 });
  assert.equal(partial.at(-1).candidates, undefined, 'Pinned SDK error conversion changed');
  await assert.rejects(consumeBusinessStream(partial), error => error.code === 'MISSING_STOP');
  before = generations;
  await assert.rejects(failedChat.sendMessage({ message: 'phase5-followup' }), error => error.status === 409);
  assert.equal(generations, before, 'Failed Chat history reached generation');
  coverage.add('failed-chat-prefix-refused-without-retry');
  process.stdout.write(JSON.stringify({ done: true, phase: 5, sdk: version, requests, generations, coverage: [...coverage],
    sdkStreamErrors: 'discarded_without_stop' }) + '\n');
}

async function phase6Worker() {
  const expectedParts = [
    { text: 'reason', thought: true, thoughtSignature: '+/8=' },
    { text: '雨A' },
    { executableCode: { language: 'PYTHON', code: 'print(21 * 2)\n' } },
    { codeExecutionResult: { outcome: 'OUTCOME_OK', output: '42\n' } },
    { text: 'B雪' },
    { inlineData: { mimeType: 'application/json+suggestions', data: Buffer.from('{"suggestions":["synthetic"]}').toString('base64') } },
    { text: '尾' },
  ];
  const grounding = {
    groundingChunks: [
      { retrievedContext: { uri: 'https://example.invalid/retrieved-one', title: 'Retrieved one' } },
      { retrievedContext: { uri: 'https://example.invalid/retrieved-two', title: 'Retrieved two' } },
    ],
    groundingSupports: [
      { segment: { partIndex: 1, startIndex: 0, endIndex: 3, text: '雨' }, groundingChunkIndices: [1] },
      { segment: { partIndex: 1, startIndex: 3, endIndex: 4, text: 'A' }, groundingChunkIndices: [0, 1] },
      { segment: { partIndex: 4, startIndex: 0, endIndex: 4, text: 'B雪' }, groundingChunkIndices: [0, 1] },
      { segment: { partIndex: 6, startIndex: 0, endIndex: 3, text: '尾' }, groundingChunkIndices: [1] },
    ],
  };
  // The Gemini wire uses citationSources; the official SDK normalizes it to
  // citations. These source-only references must not invent quote offsets.
  const citation = { citations: [{ uri: 'https://example.invalid/citation', license: 'MIT' }] };
  const user = text => ({ role: 'user', parts: [{ text }] });
  const checkMetadata = candidate => {
    assert.deepEqual(candidate.groundingMetadata, grounding);
    assert.deepEqual(candidate.citationMetadata, citation);
    assert.ok(candidate.groundingMetadata.groundingChunks.every(chunk => chunk.web === undefined));
    for (const source of candidate.citationMetadata.citations) {
      for (const absent of ['startIndex', 'endIndex', 'title', 'publicationDate']) assert.equal(source[absent], undefined);
    }
  };
  const checkChunks = chunks => {
    assert.equal(chunks.length, expectedParts.length);
    assert.deepEqual(chunks.flatMap(chunk => chunk.candidates[0].content.parts), expectedParts);
    assert.equal(chunks.at(-1).candidates[0].finishReason, 'STOP');
    assert.deepEqual(chunks.at(-1).candidates[0].content.parts, [{ text: '尾' }]);
    checkMetadata(chunks.at(-1).candidates[0]);
    assert.ok(chunks.slice(0, -1).every(chunk => chunk.candidates[0].groundingMetadata === undefined &&
      chunk.candidates[0].citationMetadata === undefined));
    assert.deepEqual(lastWireStream, { errors: [], stops: 1 });
  };
  const followup = async contents => {
    const response = await client.models.generateContent({ model, contents });
    assert.equal(response.text, 'phase6-followup-ok');
    assert.equal(response.candidates[0].finishReason, 'STOP');
  };
  const response = await client.models.generateContent({ model, contents: 'phase6-unary' });
  assert.deepEqual(response.candidates[0].content.parts, expectedParts);
  assert.equal(response.candidates[0].finishReason, 'STOP');
  assert.equal(response.usageMetadata, undefined);
  checkMetadata(response.candidates[0]);
  await followup([user('phase6-unary'), response.candidates[0].content, user('phase6-followup')]);
  const altered = structuredClone(response.candidates[0].content);
  altered.parts[2].executableCode.code = 'print(0)\n';
  await rejected([user('phase6-unary'), altered, user('phase6-followup')], 409);
  coverage.add('retrieved-context-not-web-source-identity');
  coverage.add('citationSources-sdk-citations-no-invented-offsets');
  const chunks = await stream('phase6-stream');
  checkChunks(chunks);
  await consumeBusinessStream(chunks);
  await followup([user('phase6-stream'), { role: 'model', parts: chunks.flatMap(chunk => chunk.candidates[0].content.parts) }, user('phase6-followup')]);
  coverage.add('mixed-parts-utf8-split-supports-terminal-only');
  const chat = client.chats.create({ model });
  const chatChunks = [];
  for await (const chunk of await chat.sendMessageStream({ message: 'phase6-chat-stream' })) chatChunks.push(chunk);
  checkChunks(chatChunks);
  const history = chat.getHistory(true);
  assert.equal(history[0].role, 'user');
  assert.equal(history.length, 1 + expectedParts.length);
  assert.ok(history.slice(1).every(content => content.role === 'model'));
  assert.deepEqual(history.slice(1).flatMap(content => content.parts), expectedParts);
  assert.equal((await chat.sendMessage({ message: 'phase6-followup' })).text, 'phase6-followup-ok');
  coverage.add('chat-all-parts-continued-without-candidate-metadata');
  process.stdout.write(JSON.stringify({ done: true, phase: 6, sdk: version, requests, generations, coverage: [...coverage] }) + '\n');
}

async function completionWorker() {
  const names = ['models/business-test', 'models/business-other', 'models/business-third'];
  for (const pageSize of [1, 1001]) {
    const listed = [];
    for await (const item of await client.models.list({ config: { pageSize } })) listed.push(item.name);
    assert.deepEqual(listed, names);
  }
  for (const name of names) assert.equal((await client.models.get({ model: name })).name, name);
  await assert.rejects(client.models.list({ config: { pageSize: 0 } }), error => error.status === 400);
  coverage.add('models-pagination-clamp-1001-zero-rejected');
  const uploads = [];
  for (const [index, suffix] of ['one', 'two', 'three'].entries()) {
    const bytes = Buffer.from('SDK fixture ' + suffix);
    const uploaded = await client.files.upload({ file: new Blob([bytes], { type: 'text/plain' }),
      config: { mimeType: 'text/plain', displayName: `fixture ${index + 1}.txt` } });
    assert.equal(uploaded.source, 'UPLOADED');
    assert.equal(uploaded.downloadUri, undefined, 'Uploaded files must not masquerade as generated files');
    const fetched = await client.files.get({ name: uploaded.name });
    assert.equal(fetched.sha256Hash, uploaded.sha256Hash);
    assert.equal(Number(fetched.sizeBytes), bytes.length);
    assert.equal(fetched.state, 'ACTIVE');
    assert.ok(fetched.expirationTime);
    uploads.push({ uploaded, bytes });
  }
  for (const pageSize of [1, 1001]) {
    const listed = [];
    for await (const item of await client.files.list({ config: { pageSize } })) listed.push(item.name);
    assert.equal(listed.length, uploads.length);
    assert.deepEqual(new Set(listed), new Set(uploads.map(item => item.uploaded.name)));
  }
  coverage.add('files-resumable-multiple-pages-and-uploaded-metadata');
  assert.ok(process.env.BUSINESS_SDK_OUTPUT_DIR, 'Go fixture must own the output directory');
  const downloadPath = path.join(process.env.BUSINESS_SDK_OUTPUT_DIR, 'sdk-completion-download.bin');
  try {
    await client.files.download({ file: uploads[0].uploaded.name, downloadPath });
    assert.deepEqual(fs.readFileSync(downloadPath), uploads[0].bytes);
  } finally {
    if (fs.existsSync(downloadPath)) fs.unlinkSync(downloadPath);
  }
  const fileResponse = await client.models.generateContent({ model, contents: [
    ...uploads.map(item => createPartFromUri(item.uploaded.uri, item.uploaded.mimeType)), { text: 'completion-files' }] });
  assert.equal(fileResponse.text, 'completion-files-ok');
  for (const { uploaded } of uploads) {
    await client.files.delete({ name: uploaded.name });
    await assert.rejects(client.files.get({ name: uploaded.name }), error => error.status === 404);
  }
  coverage.add('sdk-download-by-name-reference-and-delete');

  const data = Buffer.from([251, 255, 0, 1, 10]).toString('base64');
  const expected = [{ text: 'media-start' },
    { inlineData: { mimeType: 'image/png', data }, thought: true, thoughtSignature: '+/8=' },
    ...Array.from({ length: 33 }, () => ({ inlineData: { mimeType: 'audio/pcm;rate=24000', data } })),
    { inlineData: { mimeType: 'video/mp4', data } },
    { inlineData: { mimeType: 'application/pdf', data } }, { text: 'media-end' }];
  const user = text => ({ role: 'user', parts: [{ text }] });
  const followup = async contents => {
    const response = await client.models.generateContent({ model, contents });
    assert.equal(response.text, 'completion-followup-ok');
    assert.equal(response.candidates[0].finishReason, 'STOP');
  };
  const first = await client.models.generateContent({ model, contents: 'completion-media' });
  assert.deepEqual(first.candidates[0].content.parts, expected);
  assert.equal(first.candidates[0].finishReason, 'STOP');
  await followup([user('completion-media'), first.candidates[0].content, user('completion-followup')]);
  const altered = structuredClone(first.candidates[0].content);
  altered.parts[1].inlineData.data = Buffer.from('changed').toString('base64');
  await rejected([user('completion-media'), altered, user('completion-followup')], 409);
  coverage.add('binary-media-mime-annotation-and-history-integrity');
  const checkChunks = chunks => {
    assert.deepEqual(chunks.flatMap(chunk => chunk.candidates[0].content.parts), expected);
    assert.equal(chunks.at(-1).candidates[0].finishReason, 'STOP');
    assert.deepEqual(chunks.at(-1).candidates[0].content.parts, [{ text: 'media-end' }]);
    assert.deepEqual(lastWireStream, { errors: [], stops: 1 });
  };
  const chunks = await stream('completion-stream');
  checkChunks(chunks);
  await consumeBusinessStream(chunks);
  await followup([user('completion-stream'), { role: 'model', parts: chunks.flatMap(chunk => chunk.candidates[0].content.parts) }, user('completion-followup')]);
  const chat = client.chats.create({ model });
  const chatChunks = [];
  for await (const chunk of await chat.sendMessageStream({ message: 'completion-chat' })) chatChunks.push(chunk);
  checkChunks(chatChunks);
  assert.deepEqual(chat.getHistory(true).slice(1).flatMap(content => content.parts), expected);
  assert.equal((await chat.sendMessage({ message: 'completion-followup' })).text, 'completion-followup-ok');
  coverage.add('binary-media-sse-and-native-chat-roundtrip');
  await rejected('completion-invalid', 400, { temperature: -1 });
  const before = generations;
  await assert.rejects(client.models.countTokens({ model, contents: 'completion-count' }), error => error.status === 501);
  assert.equal(generations, before);
  coverage.add('mapped-invalid-and-count-unavailable-without-generation');
  process.stdout.write(JSON.stringify({ done: true, phase: 'completion', sdk: version, requests, generations,
    coverage: [...coverage] }) + '\n');
}

try {
  if (process.argv.includes('--completion-worker')) {
    await completionWorker();
  } else if (process.argv.includes('--phase6-worker')) {
    await phase6Worker();
  } else if (process.argv.includes('--phase5-worker')) {
    await phase5Worker();
  } else {
  const listed = [];
  for await (const item of await client.models.list({ config: { pageSize: 1 } })) listed.push(item);
  assert.deepEqual(listed.map(item => item.name), ['models/business-test', 'models/business-other']);
  const found = await client.models.get({ model });
  assert.deepEqual(new Set(found.supportedActions), new Set(['generateContent', 'streamGenerateContent']));
  coverage.add('models-list-pagination-get');

  await generate();
  for (const mimeType of ['text/plain', 'application/pdf', 'image/png']) {
    await generate([{ inlineData: { mimeType, data: Buffer.from('synthetic bytes').toString('base64') } }, { text: 'read' }]);
  }
  coverage.add('unary-inline-data');
  await generate('mapped', { temperature: 0, thinkingConfig: { includeThoughts: false, thinkingBudget: 0 },
    responseJsonSchema: { type: 'object', properties: { keep_snake_case: { const: 9007199254740991 } } } });
  coverage.add('mapped-zero-false-schema');
  for (const config of [
    { systemInstruction: 'ignored system' }, { topP: 0 },
    { safetySettings: [{ category: 'HARM_CATEGORY_HARASSMENT', threshold: 'BLOCK_MEDIUM_AND_ABOVE' }] },
    { tools: [{ functionDeclarations: [{ name: 'ignored_function', parameters: { type: 'object' } }] }] },
  ]) await generate('optional', config);
  coverage.add('unmapped-optional-parameters-ignored');

  for (const mode of ['thought', 'suggestions']) {
    const response = await generate(mode);
    const first = response.candidates[0].content.parts[0];
    const streamed = await stream(mode);
    const streamFirst = streamed[0].candidates[0].content.parts[0];
    assert.deepEqual(streamFirst, first);
    if (mode === 'thought') {
      assert.equal(first.thought, true);
      assert.equal(first.thoughtSignature, Buffer.from('opaque-fixture').toString('base64'));
    } else {
      assert.equal(first.text, undefined);
      assert.equal(first.inlineData.mimeType, 'application/json+suggestions');
      assert.equal(Buffer.from(first.inlineData.data, 'base64').toString(), '{"suggestions":["synthetic"],"enabled":false}');
    }
  }
  coverage.add('thought-signature-suggestions');
  const chunks = await stream('stream');
  assert.equal(chunks.map(chunk => chunk.text ?? '').join(''), 'fixture-输出');
  assert.equal(chunks.at(-1).candidates[0].finishReason, 'STOP');
  // Keeping the last real part for STOP may coalesce this two-part fixture
  // into one event. The native contract guarantees content and completion,
  // not a fixed number of transport events.
  assert.ok(chunks.length >= 1);
  assert.deepEqual(lastWireStream, { errors: [], stops: 1 });
  assert.deepEqual(await consumeBusinessStream(chunks), { events: chunks.length, completed: true });
  coverage.add('fragmented-sse-utf8-stop');

  await rejected([{ role: 'user', parts: [{ text: 'before' }] },
    { role: 'model', parts: [{ text: 'history' }] }, { role: 'user', parts: [{ text: 'after' }] }], 409);
  await rejected([{ inlineData: { mimeType: 'audio/wav', data: 'eA==' } }]);
  await rejected([{ role: 'user', parts: [{ functionResponse: { name: 'unavailable', response: {} } }] }]);
  await rejected('hello', 400, { temperature: -1 });
  // The pinned JS SDK omits null before serialization. This says nothing about
  // an explicit JSON null sent directly to the native handler.
  await generate('optional', { temperature: null });
  const beforeCount = generations;
  await assert.rejects(client.models.countTokens({ model, contents: 'hello' }), error => error.status === 501);
  assert.equal(generations, beforeCount);
  coverage.add('invalid-structure-and-unavailable-method-preflight');

  const fixtureBytes = Buffer.from('synthetic PDF file bytes');
  const uploaded = await client.files.upload({ file: new Blob([fixtureBytes], { type: 'application/pdf' }),
    config: { mimeType: 'application/pdf', displayName: 'fixture.pdf' } });
  assert.match(uploaded.name, /^files\/biz-/);
  assert.equal(uploaded.mimeType, 'application/pdf');
  assert.equal(uploaded.displayName, 'fixture.pdf');
  assert.ok(uploaded.uri.startsWith(origin + '/v1beta/files/biz-'));
  const fetched = await client.files.get({ name: uploaded.name });
  assert.equal(fetched.sha256Hash, uploaded.sha256Hash);
  assert.equal(Number(fetched.sizeBytes), fixtureBytes.length);
  const fileList = [];
  for await (const file of await client.files.list({ config: { pageSize: 1 } })) fileList.push(file);
  assert.equal(fileList.length, 1);
  await generate([createPartFromUri(uploaded.uri, uploaded.mimeType), { text: 'file reference' }]);
  const download = await pipeFetch(uploaded.uri + ':download');
  assert.equal(download.status, 200);
  assert.deepEqual(Buffer.from(await download.arrayBuffer()), fixtureBytes);
  await rejected([createPartFromUri('https://external.invalid/v1beta/' + uploaded.name, uploaded.mimeType)], 400);
  await client.files.delete({ name: uploaded.name });
  await assert.rejects(client.files.get({ name: uploaded.name }), error => error.status === 404);
  coverage.add('files-upload-get-list-reference-download-delete-ownership');

  let before = generations;
  await assert.rejects(client.models.generateContent({ model, contents: 'fail' }), error => error.status === 503);
  assert.equal(generations, before + 1, 'SDK retried failed generation');
  for (const mode of ['midfail', 'truncated']) {
    before = generations;
    // The pinned SDK silently converts the wire error to an empty SDK chunk.
    // Record that behavior; do not pretend the SDK itself rejected the stream.
    const partial = await stream(mode);
    assert.equal(generations, before + 1, 'SDK replayed partial generation');
    assert.deepEqual(lastWireStream, { errors: [502], stops: 0 }, 'Server lost its error or fabricated completion');
    assert.ok(partial.some(chunk => chunk.text === 'partial'));
    assert.ok(partial.every(chunk => !chunk.candidates?.some(candidate => candidate.finishReason === 'STOP')));
    assert.equal(partial.at(-1).candidates, undefined);
    assert.equal(partial.at(-1).error, undefined, 'Pinned SDK conversion behavior changed; re-evaluate its error contract');
    await assert.rejects(consumeBusinessStream(partial), error => error.code === 'MISSING_STOP');
  }
  await assert.rejects(consumeBusinessStream([{ error: { code: 502 } }]), error => error.code === 'UPSTREAM_STREAM_ERROR');
  await assert.rejects(consumeBusinessStream([{ candidates: [{ finishReason: 'MAX_TOKENS' }] }]),
    error => error.code === 'UNSUCCESSFUL_FINISH');
  coverage.add('errors-truncation-no-retry-no-false-stop');
  process.stdout.write(JSON.stringify({ done: true, sdk: version, requests, generations, coverage: [...coverage],
    sdkStreamErrors: 'discarded_without_stop' }) + '\n');
  }
} finally {
  lines.close();
}

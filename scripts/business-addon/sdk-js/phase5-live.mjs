// Explicit two-turn local acceptance; never invoked by the offline test suite.
// All account data, synthetic markers and model responses stay in memory.
import assert from 'node:assert/strict';
import { randomBytes } from 'node:crypto';
import fs from 'node:fs/promises';
import path from 'node:path';
import { performance } from 'node:perf_hooks';
import { parseArgs } from 'node:util';
import { consumeBusinessStream } from './stream-consumer.mjs';

const { values } = parseArgs({ options: {
  'allow-live': { type: 'boolean', default: false },
  session: { type: 'string' }, report: { type: 'string' }, phase: { type: 'string', default: '5' },
}, strict: true });
if (!values['allow-live'] || !values.session || !values.report) {
  throw new Error('Requires --allow-live --session PATH --report PATH; no credentials were read');
}
if (path.resolve(values.session).toLowerCase() === path.resolve(values.report).toLowerCase()) {
  throw new Error('The report must not replace the credential source');
}
if (!['5', '6', 'completion'].includes(values.phase)) {
  throw new Error('Phase must be 5, 6 or completion; no credentials were read');
}
const completion = values.phase === 'completion';
const phase = completion ? 'completion' : Number(values.phase);

const version = '2.22.0';
const origin = 'http://127.0.0.1:2048';
const model = 'business-gemini-4-argon';
const statusURL = `${origin}/api/addons/business/status`;
const streamURL = `${origin}/v1beta/models/${model}:streamGenerateContent?alt=sse`;
const unaryURL = `${origin}/v1beta/models/${model}:generateContent`;
const started = performance.now();
const nativeFetch = globalThis.fetch;
const report = {
  sdk: '@google/genai@2.22.0', model: 'business-gemini-4-argon',
  phase, generation_attempts: 0, status_attempts: 0, passed: false, checks: {},
};
const requiredChecks = ['baseline_ready_idle', 'first_marker_and_sum', 'sixteen_lines_and_end',
  'stream_completed', 'metadata_only_at_stop', 'citation_indices_absent', 'chat_retained_all_parts',
  'recalled_marker_sum', 'continuation_completed', 'no_synthetic_usage', 'one_session_one_continuation', 'final_ready_idle'];
if (completion) requiredChecks.push('catalog_list_get', 'local_invalid_preflight', 'file_uploaded_and_get',
  'file_listed', 'file_download_matches', 'file_deleted');
let key;
let before;
let stage = 'setup';
let client;
let uploaded;
let downloadPath;
const ownedFiles = new Set();
const uploadURLs = new Set();
let uploadStarts = 0;
let uploadWrites = 0;
let auxiliaryRequests = 0;
let cleanupRequests = 0;
const invalidBody = JSON.stringify({ contents: [{ role: 'user', parts: [{ text: 'synthetic-preflight' }] }], generationConfig: [] });

class AcceptanceError extends Error {
  constructor(code) { super('Local acceptance failed'); this.name = 'AcceptanceError'; this.code = code; }
}
function receiptError(error) {
  // No server message, header, body, prompt or dynamic string enters the report.
  const allowedNames = ['ApiError', 'AbortError', 'TimeoutError', 'SyntaxError', 'AssertionError',
    'TypeError', 'AcceptanceError', 'BusinessStreamCompletionError'];
  const value = { type: allowedNames.includes(error?.name) ? error.name : 'Error' };
  const allowedCodes = ['REQUEST_GUARD', 'STATUS_HTTP', 'STATUS_SHAPE', 'BASELINE_BUSY',
    'STREAM_SHAPE', 'STREAM_AFTER_STOP', 'STREAM_LIMIT', 'MISSING_STOP',
    'UPSTREAM_STREAM_ERROR', 'UNSUCCESSFUL_FINISH'];
  if (allowedCodes.includes(error?.code)) value.code = error.code;
  if (Number.isInteger(error?.status) && error.status >= 400 && error.status <= 599) value.http_status = error.status;
  return value;
}
const ordinaryText = parts => parts.filter(part => typeof part.text === 'string' && !part.thought)
  .map(part => part.text).join('');
const seconds = begin => Math.round((performance.now() - begin)) / 1000;
function mediaReceipt(parts) {
  const result = { image_parts: 0, audio_parts: 0, video_parts: 0, pdf_parts: 0,
    suggestions_parts: 0, binary_bytes: 0, annotated_media_parts: 0 };
  for (const part of parts ?? []) {
    if (part.inlineData === undefined) continue;
    const mime = (part.inlineData.mimeType ?? '').split(';')[0].toLowerCase();
    result.image_parts += Number(mime.startsWith('image/'));
    result.audio_parts += Number(mime.startsWith('audio/'));
    result.video_parts += Number(mime.startsWith('video/'));
    result.pdf_parts += Number(mime === 'application/pdf');
    result.suggestions_parts += Number(mime === 'application/json+suggestions');
    result.binary_bytes += Buffer.from(part.inlineData.data ?? '', 'base64').length;
    result.annotated_media_parts += Number(Boolean(part.thought || part.thoughtSignature));
  }
  return result;
}
function metadataReceipt(candidate) {
  const chunks = candidate?.groundingMetadata?.groundingChunks ?? [];
  const citations = candidate?.citationMetadata?.citations ?? [];
  return {
    retrieved_context_sources: chunks.filter(chunk => chunk.retrievedContext !== undefined).length,
    web_sources: chunks.filter(chunk => chunk.web !== undefined).length,
    citation_sources: citations.length,
    citation_indices_absent: citations.every(source => source.startIndex === undefined && source.endIndex === undefined),
    metadata_only_at_stop: (candidate?.groundingMetadata === undefined && candidate?.citationMetadata === undefined) ||
      candidate?.finishReason === 'STOP',
  };
}

async function status() {
  const response = await globalThis.fetch(statusURL, { method: 'GET', signal: AbortSignal.timeout(15000) });
  if (!response.ok) {
    const error = new AcceptanceError('STATUS_HTTP');
    error.status = response.status;
    throw error;
  }
  const data = await response.json();
  // Select only bounded counters/flags, never retain the full status in reports.
  const selected = {
    sessions: data?.conversations?.sessions_created,
    continuations: data?.conversations?.continuation_attempts,
    active: data?.capacity?.active,
    budget: data?.resources?.in_use_bytes,
    ready: data?.generation_ready,
  };
  if (![selected.sessions, selected.continuations, selected.active, selected.budget]
    .every(value => Number.isSafeInteger(value) && value >= 0) || typeof selected.ready !== 'boolean') {
    throw new AcceptanceError('STATUS_SHAPE');
  }
  return selected;
}

try {
  const installed = JSON.parse(await fs.readFile(new URL('./node_modules/@google/genai/package.json', import.meta.url), 'utf8'));
  assert.equal(installed.version, version);
  const session = JSON.parse((await fs.readFile(values.session, 'utf8')).replace(/^\uFEFF/, ''));
  key = session.api_tokens?.[0];
  assert.ok(typeof key === 'string' && key.length > 0);

  globalThis.fetch = async (input, init) => {
    const request = new Request(input, { ...init, redirect: 'error' });
    const url = new URL(request.url);
    if (url.origin !== origin || url.username || url.password) throw new AcceptanceError('REQUEST_GUARD');
    const queryOnly = allowed => [...url.searchParams.keys()].every(name => allowed.includes(name));
    let uploadStart = false;
    if (request.url === statusURL && request.method === 'GET' && report.status_attempts < 2 &&
      !request.headers.has('authorization') && !request.headers.has('x-goog-api-key')) {
      report.status_attempts += 1;
    } else if (completion && stage === 'local_preflight' && request.url === unaryURL && request.method === 'POST' &&
      report.preflight_attempts === undefined && await request.clone().text() === invalidBody) {
      report.preflight_attempts = 1;
    } else if (request.method === 'POST' &&
      ((report.generation_attempts === 0 && request.url === streamURL) ||
       (report.generation_attempts === 1 && request.url === unaryURL))) {
      report.generation_attempts += 1;
    } else if (completion && auxiliaryRequests < 64 && request.method === 'GET' &&
      ((url.pathname === '/v1beta/models' && queryOnly(['pageSize', 'pageToken'])) ||
       (url.pathname === `/v1beta/models/${model}` && url.search === '') ||
       (url.pathname === '/v1beta/files' && queryOnly(['pageSize', 'pageToken'])))) {
      auxiliaryRequests += 1;
    } else if (completion && request.url === `${origin}/upload/v1beta/files` && request.method === 'POST' && uploadStarts === 0) {
      uploadStarts += 1;
      uploadStart = true;
    } else if (completion && uploadURLs.has(request.url) && request.method === 'POST' && uploadWrites === 0) {
      uploadWrites += 1;
    } else if (completion && stage === 'cleanup' && cleanupRequests < 2 && ['GET', 'DELETE'].includes(request.method) &&
      [...ownedFiles].some(name => url.pathname === `/v1beta/${name}` && url.search === '')) {
      cleanupRequests += 1;
    } else if (completion && auxiliaryRequests < 64 && ['GET', 'DELETE'].includes(request.method) &&
      [...ownedFiles].some(name => (url.pathname === `/v1beta/${name}` && url.search === '') ||
        (request.method === 'GET' && url.pathname === `/v1beta/${name}:download` && url.search === '?alt=media'))) {
      auxiliaryRequests += 1;
    } else {
      throw new AcceptanceError('REQUEST_GUARD');
    }
    const response = await nativeFetch(request);
    if (uploadStart && response.ok) {
      const upload = new URL(response.headers.get('x-goog-upload-url'));
      if (upload.origin !== origin || upload.pathname !== '/upload/v1beta/files' || upload.username || upload.password ||
        [...upload.searchParams.keys()].some(name => !['upload_id', 'upload_protocol'].includes(name)) ||
        !upload.searchParams.get('upload_id') || upload.searchParams.get('upload_protocol') !== 'resumable') {
        throw new AcceptanceError('REQUEST_GUARD');
      }
      uploadURLs.add(upload.href);
    }
    return response;
  };
  stage = 'baseline_status';
  before = await status();
  report.checks.baseline_ready_idle = before.ready && before.active === 0 && before.budget === 0;
  if (!report.checks.baseline_ready_idle) throw new AcceptanceError('BASELINE_BUSY');

  // The pinned SDK uses global fetch; install the exact-endpoint guard first.
  const { GoogleGenAI, createPartFromUri } = await import('@google/genai');
  client = new GoogleGenAI({ apiKey: key, vertexai: false,
    httpOptions: { baseUrl: origin, apiVersion: 'v1beta',
      headers: { 'X-AIStudio-Provider': 'business' }, timeout: 310000,
      retryOptions: { attempts: 1 } } });
  const chat = client.chats.create({ model });
  const marker = (completion ? 'ARGON-COMPLETION-JS-' : `ARGON-P${phase}-JS-`) + randomBytes(8).toString('hex').toUpperCase();
  const endMarker = completion ? 'COMPLETION-JS-STREAM-END' : `PHASE${phase}-JS-STREAM-END`;
  if (completion) {
    stage = 'catalog';
    const names = [];
    for await (const item of await client.models.list({ config: { pageSize: 1 } })) names.push(item.name);
    const largePageNames = [];
    for await (const item of await client.models.list({ config: { pageSize: 1001 } })) largePageNames.push(item.name);
    const found = await client.models.get({ model });
    report.checks.catalog_list_get = names.includes('models/' + model) && found.name === 'models/' + model &&
      new Set(names).size === names.length && JSON.stringify(names) === JSON.stringify(largePageNames);
    report.catalog_models = names.length;
    stage = 'local_preflight';
    const invalid = await globalThis.fetch(unaryURL, { method: 'POST', headers: {
      'x-goog-api-key': key, 'X-AIStudio-Provider': 'business', 'Content-Type': 'application/json' }, body: invalidBody,
      signal: AbortSignal.timeout(15000) });
    report.preflight_http_status = invalid.status;
    await invalid.arrayBuffer();
    report.checks.local_invalid_preflight = invalid.status === 400;
    if (!report.checks.local_invalid_preflight) throw new AcceptanceError('REQUEST_GUARD');
    stage = 'file_setup';
    const fileBytes = Buffer.from(`Project marker: ${marker}\nSum: 42\n`);
    uploaded = await client.files.upload({ file: new Blob([fileBytes], { type: 'text/plain' }),
      config: { mimeType: 'text/plain', displayName: 'completion-synthetic.txt' } });
    if (!/^files\/biz-[A-Za-z0-9_-]+$/.test(uploaded.name) || uploaded.uri !== `${origin}/v1beta/${uploaded.name}`) {
      throw new AcceptanceError('REQUEST_GUARD');
    }
    ownedFiles.add(uploaded.name);
    const fetched = await client.files.get({ name: uploaded.name });
    report.checks.file_uploaded_and_get = uploaded.source === 'UPLOADED' && uploaded.downloadUri === undefined &&
      fetched.sha256Hash === uploaded.sha256Hash && Number(fetched.sizeBytes) === fileBytes.length;
    let seen = false;
    let listed = 0;
    for await (const item of await client.files.list({ config: { pageSize: 1001 } })) {
      listed += 1;
      seen ||= item.name === uploaded.name;
    }
    report.checks.file_listed = seen;
    report.files_listed = listed;
    await fs.mkdir(path.dirname(path.resolve(values.report)), { recursive: true });
    downloadPath = path.join(path.dirname(path.resolve(values.report)), `.completion-sdk-${randomBytes(6).toString('hex')}.tmp`);
    await client.files.download({ file: uploaded.name, downloadPath });
    report.checks.file_download_matches = (await fs.readFile(downloadPath)).equals(fileBytes);
    await fs.unlink(downloadPath);
    downloadPath = undefined;
  }
  const parts = [];
  const streamReport = { events: 0, parts: 0, text_events: 0, terminal_parts: 0,
    code_parts: 0, execution_result_parts: 0, grounding_chunks: 0, grounding_supports: 0,
    retrieved_context_sources: 0, web_sources: 0, citation_sources: 0, citation_indices_absent: true,
    metadata_only_at_stop: true, usage_present: false, finish_reason: 'not_STOP', ...mediaReceipt([]) };
  report.stream_turn = streamReport;
  let stopSeen = false;
  let firstTextAt;
  let lastTextAt;
  stage = 'stream_turn';
  const streamBegin = performance.now();
  const streamPrompt = (completion ? 'Read the attached project marker and its sum. First repeat their exact values. ' :
    `Remember this project marker exactly: ${marker}. Its sum is 42. First repeat the marker and sum. `) +
    `Then write exactly 16 numbered lines (1. through 16.), each one a natural-language sentence about rain. ` +
    `Finally write ${endMarker} on its own line. Keep the marker and sum for our next turn.`;
  await consumeBusinessStream(chat.sendMessageStream({ message: completion ?
    [createPartFromUri(uploaded.uri, uploaded.mimeType), { text: streamPrompt }] : streamPrompt }), chunk => {
    if (stopSeen) throw new AcceptanceError('STREAM_AFTER_STOP');
    streamReport.events += 1;
    streamReport.usage_present ||= chunk.usageMetadata !== undefined;
    if (chunk.candidates?.length !== 1 || !Array.isArray(chunk.candidates[0].content?.parts)) {
      throw new AcceptanceError('STREAM_SHAPE');
    }
    const candidate = chunk.candidates[0];
    const frameParts = candidate.content.parts;
    const media = mediaReceipt(frameParts);
    for (const field of Object.keys(media)) streamReport[field] += media[field];
    parts.push(...frameParts);
    if (parts.length > 4096) throw new AcceptanceError('STREAM_LIMIT');
    streamReport.parts += frameParts.length;
    streamReport.code_parts += frameParts.filter(part => part.executableCode !== undefined).length;
    streamReport.execution_result_parts += frameParts.filter(part => part.codeExecutionResult !== undefined).length;
    if (ordinaryText(frameParts)) {
      streamReport.text_events += 1;
      firstTextAt ??= performance.now();
      lastTextAt = performance.now();
    }
    if (candidate.groundingMetadata !== undefined) {
      streamReport.grounding_chunks += candidate.groundingMetadata.groundingChunks?.length ?? 0;
      streamReport.grounding_supports += candidate.groundingMetadata.groundingSupports?.length ?? 0;
    }
    const metadata = metadataReceipt(candidate);
    streamReport.retrieved_context_sources += metadata.retrieved_context_sources;
    streamReport.web_sources += metadata.web_sources;
    streamReport.citation_sources += metadata.citation_sources;
    streamReport.citation_indices_absent &&= metadata.citation_indices_absent;
    streamReport.metadata_only_at_stop &&= metadata.metadata_only_at_stop;
    if (candidate.finishReason === 'STOP') {
      stopSeen = true;
      streamReport.finish_reason = 'STOP';
      streamReport.terminal_parts = frameParts.length;
    }
  });
  const firstText = ordinaryText(parts);
  streamReport.elapsed_seconds = seconds(streamBegin);
  streamReport.first_text_seconds = firstTextAt === undefined ? 0 : Math.round(firstTextAt - streamBegin) / 1000;
  streamReport.text_span_seconds = firstTextAt === undefined ? 0 : Math.round(lastTextAt - firstTextAt) / 1000;
  streamReport.text_bytes = Buffer.byteLength(firstText);
  streamReport.numbered_lines = [...firstText.matchAll(/^\s*\d{1,2}[.)]\s+\S/gm)].length;
  report.checks.first_marker_and_sum = firstText.includes(marker) && /\b42\b/.test(firstText);
  report.checks.sixteen_lines_and_end = streamReport.numbered_lines === 16 && firstText.trim().endsWith(endMarker);
  report.checks.stream_completed = stopSeen && streamReport.terminal_parts > 0 && streamReport.text_events > 1;
  report.checks.metadata_only_at_stop = streamReport.metadata_only_at_stop;
  report.checks.citation_indices_absent = streamReport.citation_indices_absent;
  const history = chat.getHistory(true);
  assert.equal(history[0]?.role, 'user');
  assert.ok(history.length >= 2 && history.slice(1).every(content => content.role === 'model'));
  assert.deepEqual(history.slice(1).flatMap(content => content.parts), parts);
  report.checks.chat_retained_all_parts = true;

  stage = 'continuation_turn';
  const continuationBegin = performance.now();
  // The previous marker and sum are deliberately absent from this new message.
  const continuation = await chat.sendMessage({ message:
    'Recall the project marker and its sum from our previous turn. Reply only with one JSON object containing exactly the keys "marker" and "sum". No markdown or other text.' });
  const candidate = continuation.candidates?.[0];
  const secondText = ordinaryText(candidate?.content?.parts ?? []);
  const recalled = JSON.parse(secondText.trim());
  report.continuation_turn = { elapsed_seconds: seconds(continuationBegin),
    text_bytes: Buffer.byteLength(secondText),
    finish_reason: candidate?.finishReason === 'STOP' ? 'STOP' : 'not_STOP',
    usage_present: continuation.usageMetadata !== undefined, ...metadataReceipt(candidate), ...mediaReceipt(candidate?.content?.parts) };
  report.checks.recalled_marker_sum = recalled.marker === marker && recalled.sum === 42 &&
    Object.keys(recalled).sort().join(',') === 'marker,sum';
  report.checks.continuation_completed = candidate?.finishReason === 'STOP';
  report.checks.metadata_only_at_stop &&= report.continuation_turn.metadata_only_at_stop;
  report.checks.citation_indices_absent &&= report.continuation_turn.citation_indices_absent;
  report.checks.no_synthetic_usage = !streamReport.usage_present && !report.continuation_turn.usage_present;
} catch (error) {
  report.error_stage = stage;
  report.error = receiptError(error);
} finally {
  stage = 'cleanup';
  if (uploaded !== undefined && ownedFiles.has(uploaded.name)) {
    try {
      await client.files.delete({ name: uploaded.name });
      try {
        await client.files.get({ name: uploaded.name });
        report.checks.file_deleted = false;
      } catch (error) {
        report.checks.file_deleted = error?.status === 404;
        if (!report.checks.file_deleted) throw error;
      }
    } catch (error) {
      report.cleanup_error = receiptError(error);
      report.checks.file_deleted = false;
    }
  }
  if (downloadPath !== undefined) {
    try { await fs.unlink(downloadPath); } catch (error) {
      if (error?.code !== 'ENOENT') report.cleanup_error = receiptError(error);
    }
  }
  if (before !== undefined) {
    try {
      const after = await status();
      report.sessions_created_delta = after.sessions - before.sessions;
      report.continuation_attempts_delta = after.continuations - before.continuations;
      report.final_active = after.active;
      report.final_in_use_bytes = after.budget;
      report.checks.one_session_one_continuation = report.sessions_created_delta === 1 && report.continuation_attempts_delta === 1;
      report.checks.final_ready_idle = after.ready && after.active === 0 && after.budget === 0;
    } catch (error) {
      report.status_error = receiptError(error);
      report.checks.final_ready_idle = false;
    }
  }
  key = undefined;
  globalThis.fetch = nativeFetch;
  report.elapsed_seconds = seconds(started);
  if (completion) {
    report.auxiliary_requests = auxiliaryRequests;
    report.cleanup_requests = cleanupRequests;
  }
  report.passed = report.generation_attempts === 2 && report.status_attempts === 2 &&
    report.error === undefined && report.status_error === undefined && report.cleanup_error === undefined &&
    requiredChecks.every(check => report.checks[check] === true);
  await fs.mkdir(path.dirname(path.resolve(values.report)), { recursive: true });
  await fs.writeFile(values.report, JSON.stringify(report, null, 2) + '\n', 'utf8');
  process.stdout.write(JSON.stringify(report) + '\n');
}
process.exitCode = report.passed ? 0 : 1;

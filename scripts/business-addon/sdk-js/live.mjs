// Explicit, single-request live acceptance. This file is never run by npm test.
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { parseArgs } from 'node:util';
import { performance } from 'node:perf_hooks';

const { values } = parseArgs({ options: {
  'allow-live': { type: 'boolean', default: false },
  session: { type: 'string' },
  report: { type: 'string' },
}, strict: true });
if (!values['allow-live'] || !values.session || !values.report) {
  throw new Error('Requires --allow-live --session PATH --report PATH; no credentials were read');
}
if (path.resolve(values.session).toLowerCase() === path.resolve(values.report).toLowerCase()) {
  throw new Error('The report path must not replace the credential source');
}

const version = '2.22.0';
const origin = 'http://127.0.0.1:2048';
const model = 'business-gemini-4-argon';
const endpoint = `${origin}/v1beta/models/${model}:generateContent`;
const report = { sdk: `@google/genai@${version}`, origin, model, action: 'generateContent',
  started_at: new Date().toISOString(), generation_attempts: 0, passed: false };
const started = performance.now();
const nativeFetch = globalThis.fetch;
let key;

try {
  const installed = JSON.parse(await fs.readFile(new URL('./node_modules/@google/genai/package.json', import.meta.url), 'utf8'));
  assert.equal(installed.version, version);
  const session = JSON.parse((await fs.readFile(values.session, 'utf8')).replace(/^\uFEFF/, ''));
  key = session.api_tokens?.[0];
  assert.ok(typeof key === 'string' && key.length > 0, 'Missing local model key');

  globalThis.fetch = async (input, init) => {
    const request = new Request(input, { ...init, redirect: 'error' });
    if (request.url !== endpoint || request.method !== 'POST' || report.generation_attempts !== 0) {
      throw new Error('Live fixture origin, endpoint or single-request guard rejected the request');
    }
    report.generation_attempts += 1;
    return nativeFetch(request);
  };
  // This SDK version calls global fetch; install the guard before importing it.
  const { GoogleGenAI } = await import('@google/genai');
  const client = new GoogleGenAI({ apiKey: key, vertexai: false,
    httpOptions: { baseUrl: origin, apiVersion: 'v1beta',
      headers: { 'X-AIStudio-Provider': 'business' },
      timeout: 90000, retryOptions: { attempts: 1 } } });
  const response = await client.models.generateContent({ model,
    contents: 'Reply only with this exact JSON object, with no markdown: {"probe":"ARGON-JS-NATIVE-20261022","sum":42}' });
  const text = response.text ?? '';
  const parsed = JSON.parse(text.trim());
  report.expected_fields_matched = parsed.probe === 'ARGON-JS-NATIVE-20261022' && parsed.sum === 42;
  report.finish_reason = response.candidates?.[0]?.finishReason === 'STOP' ? 'STOP' : 'not_STOP';
  report.text_bytes = Buffer.byteLength(text);
  report.usage_present = response.usageMetadata !== undefined;
  report.passed = report.expected_fields_matched && report.finish_reason === 'STOP' &&
    !report.usage_present && report.generation_attempts === 1;
} catch (error) {
  // Never retain server text, headers, exception messages, or a credential.
  report.error_type = ['ApiError', 'AbortError', 'TimeoutError', 'SyntaxError', 'AssertionError', 'TypeError']
    .includes(error?.name) ? error.name : 'Error';
  if (Number.isInteger(error?.status) && error.status >= 400 && error.status <= 599) report.http_status = error.status;
} finally {
  key = undefined;
  globalThis.fetch = nativeFetch;
  report.elapsed_seconds = Math.round((performance.now() - started) / 1000 * 1000) / 1000;
  await fs.mkdir(path.dirname(path.resolve(values.report)), { recursive: true });
  await fs.writeFile(values.report, JSON.stringify(report, null, 2) + '\n', 'utf8');
  process.stdout.write(JSON.stringify(report) + '\n');
}
process.exitCode = report.passed ? 0 : 1;

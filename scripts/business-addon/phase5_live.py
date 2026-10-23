"""Opt-in two-turn Python Chat stream/file regression on fixed loopback.

Synthetic input, at most two generation requests, no retries. Code execution
and grounding are observations only: prompt requests do not prove tool mapping.
"""
from __future__ import annotations

import argparse
import importlib.metadata
import io
import json
from pathlib import Path
import re
import secrets
import time

import httpx

ORIGIN = 'http://127.0.0.1:2048'
MODEL = 'business-gemini-4-argon'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--allow-live', action='store_true')
    parser.add_argument('--session', type=Path, required=True)
    parser.add_argument('--report', type=Path, required=True)
    parser.add_argument('--phase', choices=('5', '6', 'completion'), default='5')
    args = parser.parse_args()
    completion = args.phase == 'completion'
    if not args.allow_live:
        raise SystemExit('Explicit --allow-live is required; no credentials read.')
    if args.session.resolve() == args.report.resolve():
        raise SystemExit('Report must not replace credential source.')
    if importlib.metadata.version('google-genai') != '2.32.0':
        raise SystemExit('Requires pinned google-genai==2.32.0')
    from google import genai
    from google.genai import types
    key = json.loads(args.session.read_text(encoding='utf-8-sig'))['api_tokens'][0]
    report = {'phase': args.phase if completion else int(args.phase), 'sdk': 'google-genai==2.32.0', 'model': MODEL,
              'generation_attempts': 0, 'status_attempts': 0, 'checks': {}, 'passed': False}
    started = time.monotonic()
    sdk = None
    uploaded = None
    before = None
    owned_files, upload_urls = set(), set()
    upload_starts = upload_writes = auxiliary_requests = 0
    cleanup_requests = 0
    stage = 'setup'
    invalid_body = json.dumps({'contents': [{'role': 'user', 'parts': [{'text': 'synthetic-preflight'}]}],
                               'generationConfig': []}).encode()

    def guard(request):
        nonlocal upload_starts, upload_writes, auxiliary_requests, cleanup_requests
        if (request.url.scheme, request.url.host, request.url.port) != ('http', '127.0.0.1', 2048) or request.url.username or request.url.password:
            raise RuntimeError('Fixed origin guard rejected request')
        if request.method == 'GET' and request.url.path == '/api/addons/business/status' and not request.url.query:
            if report['status_attempts'] >= 2 or request.headers.get('x-goog-api-key') or request.headers.get('authorization'):
                raise RuntimeError('Status request guard rejected request')
            report['status_attempts'] += 1
            return
        unary = '/v1beta/models/' + MODEL + ':generateContent'
        stream = '/v1beta/models/' + MODEL + ':streamGenerateContent'
        if completion and stage == 'local_preflight' and request.method == 'POST' and request.url.path == unary:
            if request.url.query or report.get('preflight_attempts', 0) != 0 or request.content != invalid_body:
                raise RuntimeError('Preflight request guard rejected request')
            report['preflight_attempts'] = 1
            return
        if request.method == 'POST' and request.url.path in (unary, stream):
            expected = stream if report['generation_attempts'] == 0 else unary
            if report['generation_attempts'] >= 2 or request.url.path != expected or (request.url.path == unary and request.url.query) or (request.url.path == stream and str(request.url.params) != 'alt=sse'):
                raise RuntimeError('Generation request guard rejected request')
            report['generation_attempts'] += 1
            return
        if request.method == 'POST' and str(request.url) == ORIGIN + '/upload/v1beta/files' and upload_starts == 0:
            upload_starts += 1
            return
        if request.method == 'POST' and str(request.url) in upload_urls and upload_writes == 0:
            upload_writes += 1
            return
        if stage == 'cleanup' and cleanup_requests < 2 and request.method in ('GET', 'DELETE') and any(
            request.url.path == '/v1beta/' + name and not request.url.query for name in owned_files):
            cleanup_requests += 1
            return
        if auxiliary_requests >= 64:
            raise RuntimeError('Auxiliary request bound exceeded')
        if completion and request.method == 'GET' and (
            (request.url.path in ('/v1beta/models', '/v1beta/files') and set(request.url.params.keys()) <= {'pageSize', 'pageToken'})
            or (request.url.path == '/v1beta/models/' + MODEL and not request.url.query)):
            auxiliary_requests += 1
            return
        if request.method in ('GET', 'DELETE') and any(
            (request.url.path == '/v1beta/' + name and not request.url.query)
            or (request.method == 'GET' and request.url.path == '/v1beta/' + name + ':download' and str(request.url.params) == 'alt=media')
            for name in owned_files):
            auxiliary_requests += 1
            return
        raise RuntimeError('Endpoint guard rejected request')

    def observe_response(response):
        if str(response.request.url) == ORIGIN + '/upload/v1beta/files' and response.is_success:
            upload = httpx.URL(response.headers.get('x-goog-upload-url', ''))
            if (upload.scheme, upload.host, upload.port, upload.path) != ('http', '127.0.0.1', 2048, '/upload/v1beta/files') or upload.username or upload.password or set(upload.params.keys()) != {'upload_id', 'upload_protocol'} or not upload.params.get('upload_id') or upload.params.get('upload_protocol') != 'resumable':
                raise RuntimeError('Upload capability guard rejected response')
            upload_urls.add(str(upload))

    http = httpx.Client(base_url=ORIGIN, trust_env=False, follow_redirects=False,
                        timeout=httpx.Timeout(310, connect=10), event_hooks={'request': [guard], 'response': [observe_response]})

    def status():
        response = http.get('/api/addons/business/status')
        response.raise_for_status()
        return response.json()

    def safe_error_type(exc):
        name = type(exc).__name__
        return name if name in {'ClientError', 'ServerError', 'APIError', 'HTTPStatusError', 'ReadTimeout',
            'ConnectTimeout', 'ConnectError', 'RuntimeError', 'ValueError', 'AssertionError', 'JSONDecodeError', 'TypeError'} else 'Error'

    def inspect(chunks, begin, captured_parts=None):
        result = {'events': 0, 'text_events': 0, 'code_parts': 0, 'result_parts': 0,
                  'grounding_chunks': 0, 'grounding_supports': 0, 'usage_present': False,
                  'retrieved_context_sources': 0, 'web_sources': 0, 'citation_sources': 0,
                  'image_parts': 0, 'audio_parts': 0, 'video_parts': 0, 'pdf_parts': 0, 'suggestions_parts': 0,
                  'binary_bytes': 0, 'annotated_media_parts': 0,
                  'citation_offsets_absent': True, 'metadata_only_at_stop': True, 'terminal_parts': 0}
        texts, times = [], []
        finished = False
        for chunk in chunks:
            result['events'] += 1
            result['usage_present'] |= chunk.usage_metadata is not None
            for candidate in chunk.candidates or []:
                if finished:
                    raise RuntimeError('Content after final response')
                parts = candidate.content.parts if candidate.content else []
                for part in parts or []:
                    if captured_parts is not None:
                        captured_parts.append(part)
                    if part.text and not part.thought:
                        texts.append(part.text)
                        times.append(round(time.monotonic() - begin, 3))
                        result['text_events'] += 1
                    result['code_parts'] += part.executable_code is not None
                    result['result_parts'] += part.code_execution_result is not None
                    if part.inline_data is not None:
                        mime = (part.inline_data.mime_type or '').split(';')[0].lower()
                        result['image_parts'] += mime.startswith('image/')
                        result['audio_parts'] += mime.startswith('audio/')
                        result['video_parts'] += mime.startswith('video/')
                        result['pdf_parts'] += mime == 'application/pdf'
                        result['suggestions_parts'] += mime == 'application/json+suggestions'
                        result['binary_bytes'] += len(part.inline_data.data or b'')
                        result['annotated_media_parts'] += bool(part.thought or part.thought_signature)
                if candidate.grounding_metadata:
                    sources = candidate.grounding_metadata.grounding_chunks or []
                    result['grounding_chunks'] += len(sources)
                    result['retrieved_context_sources'] += sum(source.retrieved_context is not None for source in sources)
                    result['web_sources'] += sum(source.web is not None for source in sources)
                    result['grounding_supports'] += len(candidate.grounding_metadata.grounding_supports or [])
                    result['metadata_only_at_stop'] &= candidate.finish_reason == types.FinishReason.STOP
                if candidate.citation_metadata:
                    citations = candidate.citation_metadata.citations or []
                    result['citation_sources'] += len(citations)
                    result['citation_offsets_absent'] &= all(source.start_index is None and source.end_index is None for source in citations)
                    result['metadata_only_at_stop'] &= candidate.finish_reason == types.FinishReason.STOP
                if candidate.finish_reason:
                    finished = True
                    result['finish_reason'] = candidate.finish_reason.value
                    result['terminal_parts'] = len(parts or [])
        result['first_text_seconds'] = times[0] if times else None
        result['text_span_seconds'] = round(times[-1] - times[0], 3) if times else 0
        result['elapsed_seconds'] = round(time.monotonic() - begin, 3)
        result['text_bytes'] = len(''.join(texts).encode())
        return ''.join(texts), result

    try:
        before = status()
        report['before_conversations'] = before['conversations']
        report['checks']['baseline_ready_idle'] = (before['generation_ready'] and before['capacity']['active'] == 0
                                                   and before['resources']['in_use_bytes'] == 0)
        if not report['checks']['baseline_ready_idle']:
            raise RuntimeError('Baseline resources are not idle')
        sdk = genai.Client(api_key=key, vertexai=False, http_options=types.HttpOptions(
            base_url=ORIGIN, api_version='v1beta', headers={'X-AIStudio-Provider': 'business'},
            httpx_client=http, retry_options=types.HttpRetryOptions(attempts=1)))
        marker = ('ARGON-COMPLETION-' if completion else f'ARGON-P{args.phase}-') + secrets.token_hex(6).upper()
        stream_end = 'COMPLETION-STREAM-END' if completion else f'PHASE{args.phase}-STREAM-END'
        chat_end = f'PHASE{args.phase}-CHAT-END'
        if completion:
            stage = 'catalog'
            names = [item.name for item in sdk.models.list(config=types.ListModelsConfig(page_size=1))]
            large = [item.name for item in sdk.models.list(config=types.ListModelsConfig(page_size=1001))]
            found = sdk.models.get(model=MODEL)
            report['catalog_models'] = len(names)
            report['checks']['catalog_list_get'] = ('models/' + MODEL in names and names == large
                and len(set(names)) == len(names) and found.name == 'models/' + MODEL)
            stage = 'local_preflight'
            invalid = http.post('/v1beta/models/' + MODEL + ':generateContent', content=invalid_body,
                headers={'Content-Type': 'application/json', 'x-goog-api-key': key, 'X-AIStudio-Provider': 'business'})
            report['preflight_http_status'] = invalid.status_code
            report['checks']['local_invalid_preflight'] = invalid.status_code == 400
            if not report['checks']['local_invalid_preflight']:
                raise RuntimeError('Local invalid request was not rejected')
        numbers = [19, 23] if completion else [i * i + 7 for i in range(1, 101)]
        expected = sum(numbers)
        fixture_bytes = (f'Project code: {marker}\nValues: ' + ', '.join(map(str, numbers)) + '\n').encode()
        stage = 'file_setup'
        uploaded = sdk.files.upload(file=io.BytesIO(fixture_bytes), config=types.UploadFileConfig(
            mime_type='text/plain', display_name=f'phase{args.phase}-synthetic.txt'))
        if not re.fullmatch(r'files/biz-[A-Za-z0-9_-]+', uploaded.name or '') or uploaded.uri != ORIGIN + '/v1beta/' + uploaded.name:
            raise RuntimeError('Uploaded file identity was not local')
        owned_files.add(uploaded.name)
        if completion:
            fetched = sdk.files.get(name=uploaded.name)
            report['checks']['file_uploaded_and_get'] = (uploaded.source == types.FileSource.UPLOADED
                and uploaded.download_uri is None and fetched.sha256_hash == uploaded.sha256_hash
                and fetched.size_bytes == len(fixture_bytes))
            listed = list(sdk.files.list(config=types.ListFilesConfig(page_size=1001)))
            report['files_listed'] = len(listed)
            report['checks']['file_listed'] = any(item.name == uploaded.name for item in listed)
            report['checks']['file_download_matches'] = sdk.files.download(file=uploaded.name) == fixture_bytes
        # No claim that optional tools/config maps to the Business widget. The
        # output inspection counts only typed parts actually received.
        chat = sdk.chats.create(model=MODEL)
        stage = 'stream_turn'
        begin = time.monotonic()
        captured_parts = []
        response, receipt = inspect(chat.send_message_stream([
            types.Part.from_uri(file_uri=uploaded.uri, mime_type='text/plain'),
            types.Part.from_text(text=f'Read the attached project code and numeric Values. Use the Python code execution tool if available to calculate their sum. Report the exact project code and the sum as plain digits. Then give 16 numbered one-sentence observations about rain. End with {stream_end}. Remember the code and sum for our next turn.'),
        ]), begin, captured_parts)
        report['stream_turn'] = receipt
        report['checks']['file_sum_and_marker'] = marker in response and str(expected) in response
        report['checks']['stream_success'] = (receipt.get('finish_reason') == 'STOP'
            and receipt['terminal_parts'] > 0 and receipt['text_events'] > 1
            and stream_end in response and receipt['metadata_only_at_stop'] and receipt['citation_offsets_absent'])
        report['checks']['chat_retained_all_parts'] = [part for content in chat.get_history(curated=True)[1:]
                                                      for part in content.parts or []] == captured_parts
        if completion:
            report['checks']['sixteen_lines_and_end'] = (len(re.findall(r'^\s*\d{1,2}[.)]\s+\S', response, re.MULTILINE)) == 16
                and response.strip().endswith(stream_end))
        stage = 'continuation_turn'
        begin = time.monotonic()
        continuation = chat.send_message('Recall the exact project code and sum from our previous attachment. Reply only with a JSON object containing exactly the keys "marker" and "sum". No markdown or other text.' if completion else
            f'From our previous attachment, repeat its exact project code and sum first. Then find the official Python documentation for hashlib.sha256, cite one official source, and briefly explain its purpose. End with {chat_end}.')
        response, receipt = inspect([continuation], begin)
        report['continuation_turn'] = receipt
        content_matched = marker in response and str(expected) in response and chat_end in response
        if completion:
            recalled = json.loads(response.strip())
            content_matched = isinstance(recalled, dict) and set(recalled) == {'marker', 'sum'} and recalled['marker'] == marker and recalled['sum'] == expected
        report['checks']['sdk_curated_history_continuation'] = (content_matched and receipt.get('finish_reason') == 'STOP'
            and receipt['metadata_only_at_stop'] and receipt['citation_offsets_absent'])
        report['checks']['no_synthetic_usage'] = not report['stream_turn']['usage_present'] and not receipt['usage_present']
    except Exception as exc:
        report['error_type'] = safe_error_type(exc)
        report['error_stage'] = stage
        code = getattr(exc, 'code', None)
        if isinstance(code, int) and 400 <= code <= 599:
            report['http_status'] = code
        # Only allowlisted fixed local diagnostics, never server content/headers.
        match = None if completion else re.search(r'Business response is malformed or incomplete \([a-z_]+\)', str(exc))
        if match:
            report['protocol_diagnostic'] = match.group(0)
    finally:
        stage = 'cleanup'
        if uploaded is not None and uploaded.name in owned_files:
            try:
                sdk.files.delete(name=uploaded.name)
                deleted = http.get('/v1beta/' + uploaded.name, headers={'x-goog-api-key': key, 'X-AIStudio-Provider': 'business'})
                report['checks']['file_deleted'] = deleted.status_code == 404
            except Exception as exc:
                report['cleanup_error_type'] = safe_error_type(exc)
                report['checks']['file_deleted'] = False
        if before is not None:
            try:
                after = status()
                report['after_conversations'] = after['conversations']
                report['checks']['one_session_one_continuation'] = (
                    after['conversations']['sessions_created'] - before['conversations']['sessions_created'] == 1
                    and after['conversations']['continuation_attempts'] - before['conversations']['continuation_attempts'] == 1)
                report['checks']['idle_resources_ready'] = (after['generation_ready']
                    and after['capacity']['active'] == 0 and after['resources']['in_use_bytes'] == 0)
            except Exception as exc:
                report['status_error_type'] = safe_error_type(exc)
                report['checks']['idle_resources_ready'] = False
        report['passed'] = (report['generation_attempts'] == 2 and 'error_type' not in report
                            and report['status_attempts'] == 2 and 'cleanup_error_type' not in report and 'status_error_type' not in report
                            and bool(report['checks']) and all(report['checks'].values()))
        report['elapsed_seconds'] = round(time.monotonic() - started, 3)
        if completion:
            report['auxiliary_requests'] = auxiliary_requests
            report['cleanup_requests'] = cleanup_requests
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
        print(json.dumps(report, ensure_ascii=False))
        if sdk is not None:
            sdk.close()
        http.close()
    return 0 if report['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())

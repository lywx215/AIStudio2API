"""Opt-in three-turn Argon native session/file/SSE acceptance on fixed loopback.

Only synthetic data; three generation attempts maximum; no retries. The model
key stays in memory and is sent only to the exact local application origin.
"""
from __future__ import annotations

import argparse
import copy
import importlib.metadata
import json
from pathlib import Path
import re
import secrets
import time

import httpx

ORIGIN = 'http://127.0.0.1:2048'
MODEL = 'business-gemini-4-argon'
PATH = '/v1beta/models/' + MODEL


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--allow-live', action='store_true')
    parser.add_argument('--session', type=Path, required=True)
    parser.add_argument('--report', type=Path, required=True)
    args = parser.parse_args()
    if not args.allow_live:
        raise SystemExit('Explicit --allow-live is required; no credentials or network used.')
    if args.session.resolve() == args.report.resolve():
        raise SystemExit('The report must not replace the credential source.')
    if importlib.metadata.version('google-genai') != '2.32.0':
        raise SystemExit('Requires pinned google-genai==2.32.0')
    from google import genai
    from google.genai import types
    key = json.loads(args.session.read_text(encoding='utf-8-sig'))['api_tokens'][0]
    headers = {'x-goog-api-key': key, 'X-AIStudio-Provider': 'business'}
    report = {'model': MODEL, 'generation_attempts': 0, 'checks': {}, 'passed': False,
              'started_at': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}
    started = time.monotonic()
    uploaded = None
    sdk = None
    def guard(request):
        if (request.url.scheme, request.url.host, request.url.port) != ('http', '127.0.0.1', 2048):
            raise RuntimeError('Request left fixed local origin')
    http = httpx.Client(base_url=ORIGIN, trust_env=False, follow_redirects=False,
                        timeout=httpx.Timeout(310, connect=10), event_hooks={'request': [guard]})
    def status():
        response = http.get('/api/addons/business/status')
        response.raise_for_status()
        return response.json()
    def send(contents, stream=False):
        body = {'contents': contents, 'safetySettings': [],
                'generationConfig': {'temperature': 0, 'maxOutputTokens': 16}}
        # The unmapped optional fields are accepted/ignored, not effect-tested.
        report['generation_attempts'] += 1
        if report['generation_attempts'] > 3:
            raise RuntimeError('Generation attempt bound exceeded')
        begin = time.monotonic()
        if not stream:
            response = http.post(PATH + ':generateContent', headers=headers, json=body)
            report['last_http_status'] = response.status_code
            if response.status_code >= 400:
                failure = response.json().get('error', {})
                # Only fixed local protocol diagnostics may enter the report.
                message = failure.get('message', '')
                if isinstance(message, str) and re.fullmatch(r'Business response is malformed or incomplete(?: \([a-z_]+\))?', message):
                    report['protocol_diagnostic'] = message
            response.raise_for_status()
            data = response.json()
            candidate = data['candidates'][0]
            if candidate.get('finishReason') != 'STOP':
                raise RuntimeError('Unary response did not complete')
            return candidate['content'], {'elapsed_seconds': round(time.monotonic()-begin, 3),
                                         'finish_reason': candidate['finishReason'], 'usage_present': 'usageMetadata' in data}
        parts, times, finish, events = [], [], None, 0
        with http.stream('POST', PATH + ':streamGenerateContent?alt=sse', headers=headers, json=body) as response:
            report['last_http_status'] = response.status_code
            response.raise_for_status()
            for line in response.iter_lines():
                if not line.startswith('data: '):
                    continue
                data = json.loads(line[6:])
                events += 1
                if 'error' in data:
                    raise RuntimeError('Native stream error')
                for candidate in data.get('candidates', []):
                    for part in candidate.get('content', {}).get('parts', []):
                        parts.append(part)
                        if part.get('text') and not part.get('thought'):
                            times.append(round(time.monotonic()-begin, 3))
                    finish = candidate.get('finishReason', finish)
        return {'role': 'model', 'parts': parts}, {
            'events': events, 'text_events': len(times), 'first_text_seconds': times[0] if times else None,
            'text_span_seconds': round(times[-1]-times[0], 3) if times else 0,
            'finish_reason': finish, 'elapsed_seconds': round(time.monotonic()-begin, 3)}
    def text(content):
        return ''.join(part.get('text', '') for part in content['parts'] if not part.get('thought'))
    try:
        before = status()
        report['before_conversations'] = before['conversations']
        sdk = genai.Client(api_key=key, vertexai=False, http_options=types.HttpOptions(
            base_url=ORIGIN, api_version='v1beta', headers={'X-AIStudio-Provider': 'business'},
            httpx_client=http, retry_options=types.HttpRetryOptions(attempts=1)))
        marker = 'ARGON-P4-' + secrets.token_hex(6).upper()
        fixture = args.report.with_suffix('.synthetic.txt')
        fixture.parent.mkdir(parents=True, exist_ok=True)
        fixture.write_text(f'Project code: {marker}\nNorth units: 23\nSouth units: 19\n', encoding='utf-8')
        uploaded = sdk.files.upload(file=fixture, config=types.UploadFileConfig(mime_type='text/plain', display_name='phase4-synthetic.txt'))
        first = {'role': 'user', 'parts': [
            {'fileData': {'fileUri': uploaded.uri, 'mimeType': 'text/plain'}},
            {'text': 'Read and remember the attached project code and unit counts for later turns. Reply only ACK-PHASE4.'}]}
        history = [first]
        result, receipt = send(history)
        report['first_turn'] = {**receipt, 'text': text(result)}
        report['checks']['initial_file_read_ack'] = 'ACK-PHASE4' in text(result)
        history += [result, {'role': 'user', 'parts': [{'text': 'From our earlier attachment, return only JSON with project_code and total_units (North plus South).'}]}]
        second_input = copy.deepcopy(history)
        result, receipt = send(history)
        report['second_turn'] = {**receipt, 'text': text(result)}
        try:
            remembered = json.loads(text(result).strip())
        except ValueError:
            remembered = None
        report['checks']['remembered_file_marker_and_sum'] = remembered == {'project_code': marker, 'total_units': 42}
        history += [result, {'role': 'user', 'parts': [{'text': 'Repeat the remembered project code and total units. Then write 16 numbered one-sentence observations about rain. End with PHASE4-STREAM-END.'}]}]
        result, receipt = send(history, stream=True)
        report['stream_turn'] = {**receipt, 'text': text(result)}
        report['checks']['native_continuation_stream'] = (marker in text(result) and bool(re.search(r'(?<!\d)42(?!\d)', text(result)))
            and 'PHASE4-STREAM-END' in text(result) and receipt['finish_reason'] == 'STOP'
            and receipt['text_events'] > 1 and receipt['text_span_seconds'] >= .1)
        after = status()
        report['after_conversations'] = after['conversations']
        report['checks']['one_session_two_continuations'] = (
            after['conversations']['sessions_created']-before['conversations']['sessions_created'] == 1
            and after['conversations']['continuation_attempts']-before['conversations']['continuation_attempts'] == 2)
        # Reusing an already-consumed historical prefix must fail locally.
        negative_body = {'contents': second_input, 'safetySettings': [],
                         'generationConfig': {'temperature': 0, 'maxOutputTokens': 16}}
        negative = http.post(PATH + ':generateContent', headers=headers, json=negative_body)
        report['consumed_prefix_http_status'] = negative.status_code
        after_negative = status()
        report['checks']['consumed_prefix_rejected_without_generation'] = (negative.status_code == 409
            and after_negative['conversations'] == after['conversations'])
        report['checks']['idle_resources_ready'] = (after_negative['generation_ready']
            and after_negative['capacity']['active'] == 0 and after_negative['resources']['in_use_bytes'] == 0)
    except Exception as exc:
        report['error_type'] = type(exc).__name__
    finally:
        if uploaded is not None:
            try:
                sdk.files.delete(name=uploaded.name)
                deleted = http.get('/v1beta/' + uploaded.name, headers=headers)
                report['checks']['file_deleted'] = deleted.status_code == 404
            except Exception as exc:
                report['cleanup_error_type'] = type(exc).__name__
                report['checks']['file_deleted'] = False
        report['passed'] = (report['generation_attempts'] == 3 and 'error_type' not in report
                            and bool(report['checks']) and all(report['checks'].values()))
        report['elapsed_seconds'] = round(time.monotonic()-started, 3)
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(json.dumps(report, ensure_ascii=False, indent=2)+'\n', encoding='utf-8')
        print(json.dumps(report, ensure_ascii=False))
        if sdk is not None:
            sdk.close()
        http.close()
    return 0 if report['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())

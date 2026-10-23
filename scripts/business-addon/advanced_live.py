"""Opt-in, bounded local Google Search + native SDK Chat acceptance.

Default execution prints the plan without reading a session or making requests.
The live mode uses only fixed loopback endpoints and makes at most two model
requests. It records hashes and structural evidence, never response text or keys.
"""
from __future__ import annotations

import argparse
import hashlib
import importlib.metadata
import json
from pathlib import Path
import re
import secrets
import time
from urllib.parse import urlsplit


ORIGIN = 'http://127.0.0.1:2048'
MODEL = 'business-gemini-4-argon'
SDK_VERSION = '2.32.0'
STATUS_PATH = '/api/addons/business/status'
MODEL_PATH = '/v1beta/models/' + MODEL
COUNT_TEXT = 'Count these synthetic words: alpha beta gamma.'
OPTIONAL_CONFIG = {'temperature': 0, 'maxOutputTokens': 64,
                   'thinkingConfig': {'includeThoughts': False, 'thinkingBudget': 0}}
# This pinned Python SDK keeps snake_case inside thinkingConfig on the wire.
# Guard the observed serialization without claiming that these options map upstream.
WIRE_OPTIONAL_CONFIG = {**OPTIONAL_CONFIG,
                       'thinkingConfig': {'include_thoughts': False, 'thinking_budget': 0}}


def compact(value):
    return json.dumps(value, ensure_ascii=False, separators=(',', ':')).encode()


def preflight_bodies():
    content = [{'role': 'user', 'parts': [{'text': 'Synthetic local validation only.'}]}]
    return {
        'google_search_null': {'contents': content, 'tools': [{'googleSearch': None}]},
        'google_search_alias_conflict': {'contents': content, 'tools': [{'googleSearch': {}, 'google_search': {}}]},
        'google_search_duplicate': {'contents': content, 'tools': [{'googleSearch': {}}, {'googleSearch': {}}]},
        'function_call_message': {'contents': [{'role': 'user', 'parts': [{'functionCall': {'name': 'synthetic', 'args': {}}}]}]},
        'function_response_message': {'contents': [{'role': 'user', 'parts': [{'functionResponse': {'name': 'synthetic', 'response': {'ok': True}}}]}]},
        'tool_config_alias_conflict': {'contents': content, 'tools': [{'googleSearch': {}}], 'toolConfig': {}, 'tool_config': {}},
    }


class GuardFailure(Exception):
    """Fixed local guard failure; dynamic request data is never in its message."""


class EndpointGuard:
    def __init__(self):
        self.stage = 'setup'
        self.status_attempts = 0
        self.generation_attempts = 0
        self.count_attempts = 0
        self.preflights = {name: compact(value) for name, value in preflight_bodies().items()}
        self.preflight_attempts = set()
        self.first_user = None
        self.tools_unchanged = False
        self.history_present = False
        self.generation_http = []

    def request(self, request):
        if (request.url.scheme, request.url.host, request.url.port) != ('http', '127.0.0.1', 2048) or request.url.username or request.url.password:
            raise GuardFailure()
        if request.method == 'GET' and request.url.path == STATUS_PATH and not request.url.query:
            if self.status_attempts >= 2 or request.headers.get('authorization') or request.headers.get('x-goog-api-key'):
                raise GuardFailure()
            self.status_attempts += 1
            return
        if request.method != 'POST' or len(request.content) > 4 * 1024 * 1024:
            raise GuardFailure()
        if self.stage in self.preflights:
            if request.url.path != MODEL_PATH + ':generateContent' or request.url.query or self.stage in self.preflight_attempts or request.content != self.preflights[self.stage]:
                raise GuardFailure()
            self.preflight_attempts.add(self.stage)
            return
        body = json.loads(request.content)
        if self.stage == 'count_tokens':
            if request.url.path != MODEL_PATH + ':countTokens' or request.url.query or self.count_attempts != 0:
                raise GuardFailure()
            if body.get('contents') != [{'role': 'user', 'parts': [{'text': COUNT_TEXT}]}]:
                raise GuardFailure()
            self.count_attempts += 1
            return
        expected = MODEL_PATH + (':streamGenerateContent' if self.generation_attempts == 0 else ':generateContent')
        if self.generation_attempts >= 2 or request.url.path != expected or self.stage not in ('stream_turn', 'continuation_turn'):
            raise GuardFailure()
        if (self.generation_attempts == 0 and str(request.url.params) != 'alt=sse') or (self.generation_attempts == 1 and request.url.query):
            raise GuardFailure()
        if body.get('tools') != [{'googleSearch': {}}] or body.get('generationConfig') != WIRE_OPTIONAL_CONFIG:
            raise GuardFailure()
        contents = body.get('contents')
        if not isinstance(contents, list) or not contents or contents[-1].get('role') != 'user':
            raise GuardFailure()
        if self.generation_attempts == 0:
            if len(contents) != 1:
                raise GuardFailure()
            self.first_user = contents[0]
        else:
            if len(contents) < 3 or contents[0] != self.first_user or not all(item.get('role') == 'model' and item.get('parts') for item in contents[1:-1]):
                raise GuardFailure()
            if any('groundingMetadata' in item or 'citationMetadata' in item for item in contents):
                raise GuardFailure()
            self.history_present = self.tools_unchanged = True
        self.generation_attempts += 1

    def response(self, response):
        if self.stage in ('stream_turn', 'continuation_turn'):
            self.generation_http.append(response.status_code)


def safe_error(exc):
    name = type(exc).__name__
    names = {'GuardFailure', 'ClientError', 'ServerError', 'APIError', 'HTTPStatusError',
             'ReadTimeout', 'ConnectTimeout', 'ConnectError', 'RuntimeError', 'ValueError',
             'AssertionError', 'JSONDecodeError', 'TypeError', 'KeyError'}
    result = {'type': name if name in names else 'Error'}
    code = getattr(exc, 'code', None)
    if isinstance(code, int) and 400 <= code <= 599:
        result['http_status'] = code
    return result


def selected_status(value):
    selected = {'sessions': value['conversations']['sessions_created'],
                'continuations': value['conversations']['continuation_attempts'],
                'active': value['capacity']['active'], 'budget': value['resources']['in_use_bytes'],
                'ready': value['generation_ready']}
    if not all(type(selected[key]) is int and selected[key] >= 0 for key in ('sessions', 'continuations', 'active', 'budget')) or type(selected['ready']) is not bool:
        raise ValueError()
    return selected


def official_pep8(uri):
    try:
        parsed = urlsplit(uri or '')
        if parsed.scheme not in ('http', 'https') or parsed.username or parsed.password:
            return False
        return ((parsed.hostname == 'peps.python.org' and parsed.path.rstrip('/') == '/pep-0008')
                or (parsed.hostname == 'www.python.org' and parsed.path.rstrip('/') == '/dev/peps/pep-0008'))
    except ValueError:
        return False


def grounding_receipt(metadata, parts):
    result = {'grounding_sources': 0, 'retrieved_context_sources': 0, 'web_sources': 0,
              'grounding_supports': 0, 'valid_supports': 0, 'official_pep8_sources': 0,
              'official_pep8_supports': 0, 'all_supports_valid': True}
    if metadata is None:
        return result
    chunks = metadata.grounding_chunks or []
    result['grounding_sources'] = len(chunks)
    official = set()
    for index, chunk in enumerate(chunks):
        result['retrieved_context_sources'] += chunk.retrieved_context is not None
        result['web_sources'] += chunk.web is not None
        source = chunk.retrieved_context if chunk.retrieved_context is not None else chunk.web
        if source is not None and official_pep8(source.uri):
            official.add(index)
    result['official_pep8_sources'] = len(official)
    for support in metadata.grounding_supports or []:
        result['grounding_supports'] += 1
        segment = support.segment
        references = support.grounding_chunk_indices or []
        valid = segment is not None and bool(references) and all(type(index) is int and 0 <= index < len(chunks) for index in references)
        if valid:
            # Proto zero defaults may be omitted by either SDK, so absent start
            # and part index mean zero; end/text still need real source data.
            part_index, start, end = segment.part_index or 0, segment.start_index or 0, segment.end_index
            valid = (type(part_index) is int and 0 <= part_index < len(parts) and type(start) is int
                     and type(end) is int and 0 <= start < end and isinstance(segment.text, str))
            if valid:
                text = parts[part_index].text
                encoded = text.encode() if isinstance(text, str) else b''
                valid = end <= len(encoded) and encoded[start:end] == segment.text.encode()
        result['all_supports_valid'] &= bool(valid)
        result['valid_supports'] += bool(valid)
        result['official_pep8_supports'] += bool(valid and official.intersection(references))
    return result


def collect(chunks, started):
    parts, ordinary_text = [], []
    digest = hashlib.sha256()
    grounding = None
    first_text_at = last_text_at = None
    result = {'events': 0, 'parts': 0, 'text_events': 0, 'thought_parts': 0, 'signature_parts': 0,
              'code_parts': 0, 'execution_result_parts': 0, 'citation_sources': 0,
              'usage_observed': False, 'metadata_only_at_stop': True, 'citation_indices_absent': True,
              'stop_observed': False, 'terminal_parts': 0, 'first_text_seconds': 0,
              'text_arrival_span_seconds': 0}
    for chunk in chunks:
        if result['stop_observed'] or len(chunk.candidates or []) != 1:
            raise ValueError()
        result['events'] += 1
        result['usage_observed'] |= chunk.usage_metadata is not None
        candidate = chunk.candidates[0]
        frame_parts = candidate.content.parts if candidate.content else []
        if not frame_parts:
            raise ValueError()
        digest.update(chunk.model_dump_json(exclude_none=True).encode())
        digest.update(b'\n')
        parts.extend(frame_parts)
        if len(parts) > 4096:
            raise ValueError()
        text_in_frame = False
        for part in frame_parts:
            result['thought_parts'] += bool(part.thought)
            result['signature_parts'] += part.thought_signature is not None
            result['code_parts'] += part.executable_code is not None
            result['execution_result_parts'] += part.code_execution_result is not None
            if part.text and not part.thought:
                text_in_frame = True
                ordinary_text.append(part.text)
        if text_in_frame:
            last_text_at = time.monotonic()
            if first_text_at is None:
                first_text_at = last_text_at
                result['first_text_seconds'] = round(first_text_at - started, 3)
            result['text_events'] += 1
        if candidate.grounding_metadata is not None:
            if grounding is not None:
                raise ValueError()
            grounding = candidate.grounding_metadata
            result['metadata_only_at_stop'] &= candidate.finish_reason == 'STOP'
        if candidate.citation_metadata is not None:
            citations = candidate.citation_metadata.citations or []
            result['citation_sources'] += len(citations)
            result['citation_indices_absent'] &= all(item.start_index is None and item.end_index is None for item in citations)
            result['metadata_only_at_stop'] &= candidate.finish_reason == 'STOP'
        if candidate.finish_reason is not None:
            if candidate.finish_reason != 'STOP':
                raise ValueError()
            result['stop_observed'] = True
            result['terminal_parts'] = len(frame_parts)
    text = ''.join(ordinary_text)
    result.update(grounding_receipt(grounding, parts))
    result.update(parts=len(parts), text_bytes=len(text.encode()), output_sha256=digest.hexdigest(),
                  elapsed_seconds=round(time.monotonic() - started, 3))
    if first_text_at is not None:
        result['text_arrival_span_seconds'] = round(last_text_at - first_text_at, 3)
    result['grounding_evidence'] = ('source_and_valid_support' if result['grounding_sources'] and result['valid_supports']
                                  else 'source_only' if result['grounding_sources'] else 'none')
    return text, parts, result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--allow-live', action='store_true')
    parser.add_argument('--session', type=Path)
    parser.add_argument('--report', type=Path)
    args = parser.parse_args()
    if not args.allow_live:
        print(json.dumps({'mode': 'plan', 'credentials_read': False, 'network_requests': 0,
            'sdk': 'google-genai==' + SDK_VERSION, 'origin': ORIGIN, 'model': MODEL,
            'maximum_generation_requests': 2, 'local_preflight_requests': len(preflight_bodies()),
            'count_tokens_requests': 1, 'expected_count_tokens_status': 501,
            'tools': [{'googleSearch': {}}], 'unmapped_options': OPTIONAL_CONFIG,
            'advanced_parameter_semantics_claimed': False,
            'search_evidence': 'grounding source with valid support; answer self-report is insufficient'}))
        return 0
    if not args.session or not args.report or args.session.resolve() == args.report.resolve():
        raise SystemExit('Live mode requires distinct --session and --report paths; no credentials read.')
    if importlib.metadata.version('google-genai') != SDK_VERSION:
        raise SystemExit('Requires pinned google-genai==2.32.0; no credentials read.')
    import httpx
    from google import genai
    from google.genai import errors, types

    guard = EndpointGuard()
    before = None
    sdk = None
    started = time.monotonic()
    report = {'sdk': 'google-genai==' + SDK_VERSION, 'model': MODEL, 'passed': False, 'checks': {},
              'preflights': {}, 'unmapped_options_requested': OPTIONAL_CONFIG,
              'advanced_parameter_semantics_claimed': False, 'token_estimate_produced': False}
    required = ['baseline_idle', 'local_preflights', 'count_tokens_unimplemented', 'first_marker_and_fact',
                'stream_completed', 'grounding_valid', 'search_grounding_observed', 'official_pep8_supported',
                'history_retained_all_parts', 'continuation_recalled', 'continuation_completed',
                'tools_unchanged_on_wire', 'no_synthetic_usage', 'one_session_one_continuation', 'final_idle']
    key = None
    http = httpx.Client(base_url=ORIGIN, trust_env=False, follow_redirects=False,
        timeout=httpx.Timeout(310, connect=10), event_hooks={'request': [guard.request], 'response': [guard.response]})

    def status():
        response = http.get(STATUS_PATH)
        response.raise_for_status()
        return selected_status(response.json())

    try:
        key = json.loads(args.session.read_text(encoding='utf-8-sig'))['api_tokens'][0]
        if not isinstance(key, str) or not key:
            raise ValueError()
        guard.stage = 'baseline_status'
        before = status()
        report['checks']['baseline_idle'] = before['ready'] and before['active'] == 0 and before['budget'] == 0
        if not report['checks']['baseline_idle']:
            raise ValueError()
        sdk = genai.Client(api_key=key, vertexai=False, http_options=types.HttpOptions(
            base_url=ORIGIN, api_version='v1beta', headers={'X-AIStudio-Provider': 'business'},
            httpx_client=http, retry_options=types.HttpRetryOptions(attempts=1)))
        for name, body in guard.preflights.items():
            guard.stage = name
            response = http.post(MODEL_PATH + ':generateContent', content=body, headers={
                'x-goog-api-key': key, 'X-AIStudio-Provider': 'business', 'Content-Type': 'application/json'})
            report['preflights'][name] = {'http_status': response.status_code, 'rejected_locally': response.status_code == 400}
            if response.status_code != 400:
                raise ValueError()
        report['checks']['local_preflights'] = len(report['preflights']) == len(guard.preflights)
        guard.stage = 'count_tokens'
        try:
            sdk.models.count_tokens(model=MODEL, contents=COUNT_TEXT)
            report['checks']['count_tokens_unimplemented'] = False
        except errors.APIError as exc:
            report['count_tokens_http_status'] = exc.code if isinstance(exc.code, int) else 0
            report['checks']['count_tokens_unimplemented'] = exc.code == 501
        if not report['checks']['count_tokens_unimplemented']:
            raise ValueError()
        config = types.GenerateContentConfig(tools=[types.Tool(google_search=types.GoogleSearch())],
            temperature=0, max_output_tokens=64,
            thinking_config=types.ThinkingConfig(include_thoughts=False, thinking_budget=0),
            automatic_function_calling=types.AutomaticFunctionCallingConfig(disable=True), automatic_continuation=False)
        chat = sdk.chats.create(model=MODEL, config=config)
        marker = 'ARGON-SEARCH-' + secrets.token_hex(8).upper()
        prompt = ('Use Google Search to verify the official PEP 8 Maximum Line Length recommendation for Python code, '
                  'not the separate docstring/comment limit. Cite the official PEP 8 web source and state its integer '
                  'character limit. Keep the answer under 120 words. Include and remember this synthetic project '
                  f'marker exactly: {marker}. Remember the verified code line limit for our next turn.')
        guard.stage = 'stream_turn'
        text, parts, receipt = collect(chat.send_message_stream(prompt), time.monotonic())
        report['stream_turn'] = receipt
        checks = report['checks']
        checks['first_marker_and_fact'] = marker in text and re.search(r'(?<!\d)79(?!\d)', text) is not None
        checks['stream_completed'] = receipt['stop_observed'] and receipt['terminal_parts'] > 0 and receipt['text_events'] > 1
        checks['grounding_valid'] = receipt['all_supports_valid'] and receipt['metadata_only_at_stop'] and receipt['citation_indices_absent']
        checks['search_grounding_observed'] = receipt['grounding_sources'] > 0 and receipt['valid_supports'] > 0
        checks['official_pep8_supported'] = receipt['official_pep8_supports'] > 0
        history = chat.get_history(curated=True)
        checks['history_retained_all_parts'] = (len(history) >= 2 and history[0].role == 'user'
            and all(item.role == 'model' for item in history[1:]) and [part for item in history[1:] for part in item.parts or []] == parts)
        if not checks['stream_completed'] or not checks['history_retained_all_parts']:
            raise ValueError()
        guard.stage = 'continuation_turn'
        continuation_started = time.monotonic()
        response = chat.send_message('From our preceding turn, recall the synthetic project marker and the verified '
            'code line character limit. Reply only with one JSON object with exactly keys "marker" and "line_limit" '
            '(integer). No markdown or other text.')
        text, _, receipt = collect([response], continuation_started)
        report['continuation_turn'] = receipt
        recalled = json.loads(text.strip())
        checks['continuation_recalled'] = (isinstance(recalled, dict) and set(recalled) == {'marker', 'line_limit'}
            and recalled['marker'] == marker and type(recalled['line_limit']) is int and recalled['line_limit'] == 79)
        checks['continuation_completed'] = receipt['stop_observed'] and receipt['terminal_parts'] > 0
        checks['grounding_valid'] &= receipt['all_supports_valid'] and receipt['metadata_only_at_stop'] and receipt['citation_indices_absent']
        checks['tools_unchanged_on_wire'] = guard.tools_unchanged and guard.history_present
        checks['no_synthetic_usage'] = not report['stream_turn']['usage_observed'] and not receipt['usage_observed']
    except Exception as exc:
        report['error'] = safe_error(exc)
        report['error_stage'] = guard.stage
    finally:
        if before is not None:
            guard.stage = 'final_status'
            try:
                after = status()
                report['sessions_created_delta'] = after['sessions'] - before['sessions']
                report['continuation_attempts_delta'] = after['continuations'] - before['continuations']
                report['checks']['one_session_one_continuation'] = report['sessions_created_delta'] == 1 and report['continuation_attempts_delta'] == 1
                report['checks']['final_idle'] = after['ready'] and after['active'] == 0 and after['budget'] == 0
                report['final_active'], report['final_in_use_bytes'] = after['active'], after['budget']
            except Exception as exc:
                report['status_error'] = safe_error(exc)
        report.update(generation_attempts=guard.generation_attempts, generation_http_statuses=guard.generation_http,
            status_attempts=guard.status_attempts, preflight_attempts=len(guard.preflight_attempts),
            count_tokens_attempts=guard.count_attempts, elapsed_seconds=round(time.monotonic() - started, 3))
        report['passed'] = ('error' not in report and 'status_error' not in report
            and guard.generation_attempts == 2 and guard.count_attempts == 1 and len(guard.preflight_attempts) == len(guard.preflights)
            and guard.status_attempts == 2 and all(report['checks'].get(name) is True for name in required))
        key = None
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(json.dumps(report, indent=2) + '\n', encoding='utf-8')
        print(json.dumps(report))
        if sdk is not None:
            sdk.close()
        http.close()
    return 0 if report['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())

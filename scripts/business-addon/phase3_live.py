"""One explicitly authorized Argon stream through the local Gemini API.

Exercises optional-parameter compatibility, not upstream parameter semantics.
No retries, account mutations, automatic probes or secret output.
"""
import argparse
import json
from pathlib import Path
import time
import httpx


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--session', type=Path, required=True)
    parser.add_argument('--report', type=Path, required=True)
    args = parser.parse_args()
    key = json.loads(args.session.read_text(encoding='utf-8-sig'))['api_tokens'][0]
    report = {'generation_calls': 0, 'model': 'business-gemini-4-argon', 'checks': {}, 'passed': False,
              'parameter_semantics': 'Unmapped optional parameters ignored; successful HTTP is not effect verification.'}
    started = time.monotonic()
    def guard(request):
        if (request.url.scheme, request.url.host, request.url.port) != ('http', '127.0.0.1', 2048):
            raise RuntimeError('Request left authorized loopback origin')
    try:
        with httpx.Client(base_url='http://127.0.0.1:2048', trust_env=False, follow_redirects=False,
                          timeout=httpx.Timeout(310, connect=10), event_hooks={'request': [guard]}) as client:
            info = client.get('/api/addons/business/browser-info')
            catalog = client.get('/api/addons/business/observations')
            report['checks']['management_routes'] = info.status_code == 200 and catalog.status_code == 200
            report['catalog_count'] = len(catalog.json().get('entries', []))
            report['generation_calls'] = 1
            times, texts, finish = [], [], None
            body = {'contents': [{'role': 'user', 'parts': [{'text': 'Start with PHASE3-BEGIN. Write exactly 24 numbered one-sentence observations about rain. End with PHASE3-END.'}]}],
                    'safetySettings': [{'category': 'HARM_CATEGORY_DANGEROUS_CONTENT', 'threshold': 'BLOCK_ONLY_HIGH'}],
                    'generationConfig': {'temperature': 0, 'maxOutputTokens': 8}}
            with client.stream('POST', '/v1beta/models/business-gemini-4-argon:streamGenerateContent?alt=sse',
                               headers={'x-goog-api-key': key, 'X-AIStudio-Provider': 'business'}, json=body) as response:
                report['http_status'] = response.status_code
                if response.status_code != 200:
                    raise RuntimeError('Local generation endpoint returned non-200')
                for line in response.iter_lines():
                    if not line.startswith('data: '):
                        continue
                    event = json.loads(line[6:])
                    if 'error' in event:
                        raise RuntimeError('Native stream error event')
                    for candidate in event.get('candidates', []):
                        text = ''.join(p.get('text', '') for p in candidate.get('content', {}).get('parts', []) if not p.get('thought'))
                        if text:
                            texts.append(text)
                            times.append(round(time.monotonic() - started, 3))
                        finish = candidate.get('finishReason', finish)
            text = ''.join(texts)
            report.update(text_events=len(texts), first_text_seconds=times[0] if times else None,
                          text_span_seconds=round(times[-1] - times[0], 3) if times else 0,
                          text_bytes=len(text.encode()), finish_reason=finish)
            report['checks']['stream_incremental_stop'] = len(texts) > 1 and times[-1] - times[0] >= .1 and finish == 'STOP'
            report['checks']['requested_markers'] = 'PHASE3-BEGIN' in text and 'PHASE3-END' in text
            report['checks']['unmapped_optional_parameters_accepted'] = report['http_status'] == 200
            after = client.get('/api/addons/business/status').json()
            report['checks']['idle_and_resources_released'] = after['capacity']['active'] == 0 and after['resources']['in_use_bytes'] == 0
            report['checks']['account_ready'] = after['generation_ready'] is True
            report['passed'] = all(report['checks'].values())
    except Exception as exc:
        report['error_type'] = type(exc).__name__
    finally:
        report['elapsed_seconds'] = round(time.monotonic() - started, 3)
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding='utf-8')
        print(json.dumps(report, ensure_ascii=False))
    return 0 if report['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())

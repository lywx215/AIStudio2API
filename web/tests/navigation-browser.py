"""Route smoke tests against the isolated usage fixture with synthetic management responses."""
import argparse
import json
from pathlib import Path
from urllib.parse import urlparse

from playwright.sync_api import expect, sync_playwright


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--url-file', required=True)
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    base = Path(args.url_file).read_text().strip()
    assert base.startswith('http://127.0.0.1:'), 'Only the isolated loopback fixture is supported'
    output = Path(args.output)
    output.mkdir(parents=True, exist_ok=True)
    checks, errors, model_requests = [], [], []
    session = {'enabled': True, 'authenticated': True, 'username': 'Synthetic administrator'}
    status = {
        'state': 'STOPPED', 'running': False, 'ready': False, 'version': 'navigation-fixture',
        'active_requests': 0, 'accounts': {'total': 0, 'ready': 0, 'busy': 0, 'cooldown': 0, 'auth_required': 0},
    }
    responses = {
        '/api/status': status,
        '/api/accounts': {'accounts': []},
        '/api/models': {'models': []},
        '/api/cooldowns': {'cooldowns': []},
        '/api/requests': {'requests': []},
        '/api/config': {'proxy_api_key': '', 'upstream_channels': ['playground', 'build'], 'routing_strategy': 'round-robin'},
        '/api/update': {'current': 'navigation-fixture', 'available': False, 'mode': 'development', 'updating': False},
        '/api/prompts': {'prompts': []},
        '/api/management-keys': {'data': {'items': []}, 'requestId': 'req_11111111-1111-4111-8111-111111111111'},
    }
    calls = []
    with sync_playwright() as playwright:
        browser = playwright.chromium.launch(channel='chrome', headless=True)
        context = browser.new_context(viewport={'width': 1440, 'height': 1000})

        def handle(route):
            url = urlparse(route.request.url)
            if f'{url.scheme}://{url.netloc}' != base:
                route.abort()
                return
            if url.path.startswith(('/v1/', '/v1beta/')):
                model_requests.append(url.path)
                route.abort()
            elif url.path == '/api/auth/session':
                route.fulfill(json=session)
            elif url.path == '/api/events':
                route.fulfill(content_type='text/event-stream', body=': synthetic fixture\n\n')
            elif url.path in responses:
                calls.append(url.path)
                headers = {'Cache-Control': 'no-store'} if url.path == '/api/management-keys' else {}
                route.fulfill(json=responses[url.path], headers=headers)
            else:
                route.continue_()

        context.route('**/*', handle)
        page = context.new_page()
        page.on('pageerror', lambda error: errors.append(str(error)))
        page.goto(base + '/prompts/new_chat', wait_until='domcontentloaded')
        page.locator('.makersuite-layout').wait_for()
        expect(page).to_have_title('API 试用 | AIStudio2API')
        page.reload(wait_until='domcontentloaded')
        page.locator('.makersuite-layout').wait_for()
        checks.append('direct Playground link and reload serve the production SPA')
        page.screenshot(path=str(output / 'playground.png'), full_page=True, animations='disabled')

        page.get_by_role('link', name='管理 Key', exact=True).click()
        page.get_by_role('heading', name='管理 Key', exact=True).wait_for()
        expect(page.get_by_role('button', name='创建管理 Key', exact=True)).to_be_enabled()
        assert urlparse(page.url).query == 'tab=management-keys'
        assert '/api/management-keys' in calls
        checks.append('Playground management entry preserves the password-session gate')
        page.screenshot(path=str(output / 'management-keys.png'), full_page=True, animations='disabled')
        page.go_back()
        page.locator('.makersuite-layout').wait_for()
        page.go_forward()
        page.get_by_role('heading', name='管理 Key', exact=True).wait_for()
        page.get_by_role('button', name='API 试用', exact=True).click()
        page.locator('.makersuite-layout').wait_for()
        checks.append('console and Playground navigation plus browser back/forward preserve routes')

        session['authenticated'] = False
        page.evaluate("window.dispatchEvent(new Event('admin-session-expired'))")
        page.locator('input[autocomplete="username"]').wait_for()
        expect(page.locator('.makersuite-layout')).to_have_count(0)
        assert urlparse(page.url).path == '/login'
        checks.append('session expiry unloads the independent Playground and returns to login')
        assert not model_requests, model_requests
        assert not errors, errors
        checks.append('no browser exceptions or model API requests occur')
        context.close()
        browser.close()
    result = {'checks': checks, 'errors': errors, 'model_requests': model_requests}
    (output / 'navigation-results.json').write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding='utf-8')
    print(json.dumps(result, ensure_ascii=False))


if __name__ == '__main__':
    main()

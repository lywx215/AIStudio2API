"""Real-browser acceptance against TestUsageBrowserServer's synthetic ledger only.

Requires an existing Python Playwright installation and Chrome. No model API calls.
"""
import argparse
import json
import statistics
from pathlib import Path
from urllib.parse import parse_qs, urlparse

from playwright.sync_api import sync_playwright, expect


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--url-file', required=True)
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    base = Path(args.url_file).read_text().strip()
    assert base.startswith('http://127.0.0.1:'), 'Only the isolated loopback fixture is supported'
    output = Path(args.output)
    output.mkdir(parents=True, exist_ok=True)
    results = {'loading': {}, 'checks': []}
    with sync_playwright() as playwright:
        browser = playwright.chromium.launch(channel='chrome', headless=True)
        for version, prefix in [('before', '/before/'), ('after', '/')]:
            cold, warm = [], []
            for _ in range(5):
                context = browser.new_context(viewport={'width': 1440, 'height': 1000})
                page = context.new_page()
                for label, samples in [('cold', cold), ('warm', warm)]:
                    page.goto(base + prefix + '?tab=usage', wait_until='domcontentloaded')
                    page.locator('article data').first.wait_for()
                    samples.append(page.evaluate('''() => ({
                      ready_ms: performance.now(),
                      js_bytes: performance.getEntriesByType('resource').filter(x => x.name.includes('/assets/') && x.name.includes('.js')).reduce((n,x) => n+x.decodedBodySize,0),
                      charts: performance.getEntriesByType('resource').some(x => /UsageCharts/.test(x.name)),
                      resources: performance.getEntriesByType('resource').filter(x => x.name.includes('/assets/')).map(x => ({name:x.name.split('/').pop(),bytes:x.decodedBodySize,transfer:x.transferSize}))
                    })'''))
                context.close()
            results['loading'][version] = {
                'cold_median_ms': statistics.median(x['ready_ms'] for x in cold),
                'warm_median_ms': statistics.median(x['ready_ms'] for x in warm),
                'js_bytes': cold[-1]['js_bytes'], 'samples': {'cold': cold, 'warm': warm},
            }
        (output / 'browser-results.json').write_text(json.dumps(results, ensure_ascii=False, indent=2), encoding='utf-8')

        context = browser.new_context(viewport={'width': 1440, 'height': 1000})
        page = context.new_page()
        errors, requests, loaded_assets = [], [], []
        page.on('pageerror', lambda error: errors.append(str(error)))
        page.on('request', lambda request: requests.append(request.url) if '/api/usage' in request.url else None)
        page.on('request', lambda request: loaded_assets.append(request.url) if '/assets/' in request.url else None)
        page.clock.install()
        page.goto(base + '/?tab=usage', wait_until='domcontentloaded')
        page.locator('#usage-panel-overview article data').first.wait_for()
        assert len(requests) == 1 and 'view=overview' in requests[0], requests
        assert not page.evaluate("performance.getEntriesByType('resource').some(x => /UsageCharts|RankingTable|RecordsTable|RecordDrawer/.test(x.name))")
        results['checks'].append('overview fetches only its scoped report; no charts, rankings, records or drawer code')
        page.screenshot(path=str(output / 'overview.png'), full_page=True)

        page.get_by_role('tab', name='维度统计', exact=True).click()
        page.locator('#usage-panel-dimensions tbody tr').first.wait_for()
        assert parse_qs(urlparse(requests[-1]).query)['dimension'] == ['account']
        assert not any('view=breakdown' in url for url in requests)
        page.get_by_role('button', name='展开明细', exact=True).first.click()
        page.wait_for_function("document.querySelector('#usage-panel-dimensions tbody').textContent.includes('gemini-')")
        assert sum('view=breakdown' in url for url in requests) == 1
        results['checks'].append('dimensions query only the selected dimension; breakdown fetched only on expansion')
        page.screenshot(path=str(output / 'dimensions.png'), full_page=True)

        count = len(requests)
        page.get_by_role('tab', name='概览', exact=True).click()
        page.locator('#usage-panel-overview article data').first.wait_for()
        assert len(requests) == count
        results['checks'].append('cached subpage switch issues no request')

        page.get_by_role('tab', name='趋势分析', exact=True).click()
        page.locator('#usage-panel-trends canvas').first.wait_for()
        assert 'view=trends' in requests[-1]
        before_canvases = page.locator('canvas').count()
        page.locator('#usage-panel-trends figure').last.scroll_into_view_if_needed()
        page.wait_for_function(f"document.querySelectorAll('canvas').length > {before_canvases}")
        results['checks'].append('trend charts mount as they enter the viewport')
        page.screenshot(path=str(output / 'trends.png'), full_page=True)

        page.get_by_role('tab', name='请求明细', exact=True).click()
        page.locator('#usage-panel-records tbody tr').first.wait_for()
        assert any('view=records-meta' in url for url in requests)
        assert any('/api/usage/records?' in url for url in requests)
        assert not any('RecordDrawer' in url for url in loaded_assets)
        page.get_by_role('button', name='下一页', exact=True).click()
        page.get_by_text('浏览历史页时已暂停自动刷新', exact=True).wait_for()
        page.locator('#usage-panel-records tbody tr').first.wait_for()
        count = len(requests)
        page.clock.fast_forward(31000)
        page.wait_for_timeout(20)
        assert len(requests) == count, requests[count:]
        results['checks'].append('records page two pauses automatic refresh')

        page.get_by_role('button', name='刷新数据', exact=True).click()
        page.get_by_role('button', name='上一页', exact=True).wait_for(state='visible')
        expect(page.get_by_role('button', name='上一页', exact=True)).to_be_disabled()
        page.locator('#usage-panel-records tbody tr').first.wait_for()
        count = len(requests)
        page.clock.fast_forward(31000)
        page.wait_for_timeout(30)
        assert len(requests) > count
        assert all('view=records-meta' in url or '/api/usage/records?' in url for url in requests[count:])
        results['checks'].append('manual refresh returns to page one; timer refreshes only current records page')
        page.locator('#usage-panel-records[aria-busy="false"] tbody tr').first.wait_for()
        page.evaluate("Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => window.__visibility || 'visible' }); window.__visibility = 'hidden'")
        count = len(requests)
        page.clock.fast_forward(61000)
        page.wait_for_timeout(20)
        assert len(requests) == count
        page.evaluate("window.__visibility = 'visible'; document.dispatchEvent(new Event('visibilitychange'))")
        page.wait_for_function("document.querySelector('#usage-panel-records')?.getAttribute('aria-busy') === 'false'")
        assert len(requests) > count
        assert sum('view=records-meta' in url for url in requests[count:]) == 1
        results['checks'].append('hidden page pauses refresh and resumes with one refresh, without catch-up bursts')
        page.locator('#usage-panel-records tbody tr button').first.click()
        page.get_by_role('dialog').wait_for()
        assert any('RecordDrawer' in url for url in loaded_assets), loaded_assets
        results['checks'].append('request detail code loads only when opened')
        page.screenshot(path=str(output / 'records-detail.png'), full_page=True)
        assert not errors, errors
        context.close()

        # Direct links and browser history restore the selected subpage.
        context = browser.new_context(viewport={'width': 1440, 'height': 1000})
        page = context.new_page()
        page.goto(base + '/?tab=usage&usage_view=records', wait_until='domcontentloaded')
        page.locator('#usage-panel-records tbody tr').first.wait_for()
        page.get_by_role('tab', name='概览', exact=True).click()
        page.locator('#usage-panel-overview article data').first.wait_for()
        page.go_back()
        page.locator('#usage-panel-records tbody tr').first.wait_for()
        results['checks'].append('direct subpage links and browser back restore records')
        context.close()
        browser.close()
    (output / 'browser-results.json').write_text(json.dumps(results, ensure_ascii=False, indent=2), encoding='utf-8')
    print(json.dumps({key: value for key, value in results.items() if key != 'loading'}, ensure_ascii=False))
    print(json.dumps({name: {key: value for key, value in result.items() if key != 'samples'} for name, result in results['loading'].items()}, ensure_ascii=False))


if __name__ == '__main__':
    main()

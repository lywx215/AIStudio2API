"""Bounded, explicitly authorized local Argon and resource hot-update checks.

Exactly one generation; no retries or artificial upstream quota/auth failures.
The existing model key stays in memory and is sent only to fixed loopback.
"""
from __future__ import annotations

import argparse
import copy
import json
from pathlib import Path
import time

import httpx

ORIGIN = "http://127.0.0.1:2048"
MODEL = "business-gemini-4-argon"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--session", type=Path, required=True)
    parser.add_argument("--report", type=Path, required=True)
    args = parser.parse_args()
    key = json.loads(args.session.read_text(encoding="utf-8-sig"))["api_tokens"][0]
    if not isinstance(key, str) or not key:
        raise SystemExit("Existing local model key is missing")
    started = time.monotonic()
    report = {"origin": ORIGIN, "model": MODEL, "generation_calls": 0,
              "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "checks": {}}
    pending = original = None

    def guard(request):
        if (request.url.scheme, request.url.host, request.url.port) != ("http", "127.0.0.1", 2048):
            raise RuntimeError("Request left authorized loopback origin")

    def comparable(config):
        return {k: v for k, v in config.items() if k != "revision"}

    with httpx.Client(base_url=ORIGIN, trust_env=False, follow_redirects=False,
                      timeout=httpx.Timeout(310, connect=10), event_hooks={"request": [guard]}) as http:
        def get(path):
            response = http.get(path)
            if response.status_code != 200:
                raise RuntimeError(f"Local management read failed: HTTP {response.status_code}")
            return response.json()

        def restore():
            nonlocal pending
            if pending is None:
                return
            current = get("/api/addons/business/config")
            if current["revision"] != pending["revision"] or comparable(current) != comparable(pending):
                raise RuntimeError("Concurrent config edit prevents restoration")
            restored = copy.deepcopy(original)
            restored["revision"] = current["revision"]
            response = http.put("/api/addons/business/config", json=restored)
            if response.status_code != 200:
                raise RuntimeError("Resource budget restoration failed")
            pending = None

        try:
            original = get("/api/addons/business/config")
            before = get("/api/addons/business/status")
            if not before.get("generation_ready") or before["resources"]["in_use_bytes"]:
                raise RuntimeError("Local Business is not idle and ready")
            invalid = copy.deepcopy(original)
            invalid["max_inflight_bytes"] = 0
            response = http.put("/api/addons/business/config", json=invalid)
            report["checks"]["invalid_budget_rejected_without_revision_change"] = (
                response.status_code == 400 and get("/api/addons/business/config") == original)

            headers = {"x-goog-api-key": key, "X-AIStudio-Provider": "business"}
            response = http.post(f"/v1beta/models/{MODEL}:generateContent", headers=headers, content=b"{")
            after_invalid = get("/api/addons/business/status")
            report["checks"]["invalid_json_no_account_penalty_or_resource_leak"] = (
                response.status_code == 400 and before["account_states"] == after_invalid["account_states"]
                and after_invalid["resources"]["in_use_bytes"] == 0)

            times, text_times, text, finish = [], [], [], None
            report["generation_calls"] = 1
            with http.stream("POST", f"/v1beta/models/{MODEL}:streamGenerateContent?alt=sse",
                headers=headers, json={"contents": [{"parts": [{"text":
                    "Start with PHASE2-BEGIN. Write exactly 24 numbered one-sentence observations about rain. End with PHASE2-END."}]}]}) as stream:
                if stream.status_code != 200:
                    raise RuntimeError(f"Argon stream failed: HTTP {stream.status_code}")
                for line in stream.iter_lines():
                    if not line.startswith("data: "):
                        continue
                    event = json.loads(line[6:])
                    if "error" in event:
                        raise RuntimeError("Argon stream returned a native error event")
                    times.append(round(time.monotonic() - started, 3))
                    for candidate in event.get("candidates", []):
                        value = "".join(p.get("text", "") for p in candidate.get("content", {}).get("parts", []) if not p.get("thought"))
                        if value:
                            text.append(value)
                            text_times.append(times[-1])
                        finish = candidate.get("finishReason", finish)
                    if pending is None:
                        during = get("/api/addons/business/status")
                        report["during_resources"] = during["resources"]
                        updated = copy.deepcopy(original)
                        updated["max_inflight_bytes"] = 64 << 20
                        saved = http.put("/api/addons/business/config", json=updated)
                        if saved.status_code != 200:
                            raise RuntimeError("Resource hot update failed")
                        pending = saved.json()
                        lowered = get("/api/addons/business/status")
                        report["checks"]["live_budget_accounting_and_hot_update"] = (
                            during["resources"]["in_use_bytes"] >= 32 << 20
                            and lowered["resources"]["limit_bytes"] == 64 << 20
                            and lowered["capacity"]["active"] == 1)
            value = "".join(text)
            span = text_times[-1]-text_times[0] if text_times else 0
            report["stream"] = {"events": len(times), "text_events": len(text_times), "finish_reason": finish,
                                "first_event_seconds": times[0] if times else None,
                                "text_arrival_span_seconds": round(span, 3), "text_bytes": len(value.encode())}
            report["checks"]["incremental_stream_completed_during_hot_update"] = (
                len(text_times) > 1 and span >= .1 and finish == "STOP"
                and "PHASE2-BEGIN" in value and "PHASE2-END" in value)
            final = get("/api/addons/business/status")
            report["checks"]["all_reservations_released_and_account_ready"] = (
                final["resources"]["in_use_bytes"] == 0 and final["capacity"]["active"] == 0
                and final["generation_ready"] and all(a["state"] == "ready" and a.get("consecutive_failures", 0) == 0 for a in final["account_states"]))
            restore()
            report["checks"]["config_restored"] = comparable(get("/api/addons/business/config")) == comparable(original)
            report["passed"] = all(report["checks"].values())
        except Exception as exc:
            report["passed"] = False
            report["error_type"] = type(exc).__name__
            if isinstance(exc, RuntimeError):
                report["error"] = str(exc)
        finally:
            try:
                restore()
            except Exception as exc:
                report["passed"] = False
                report["restore_error_type"] = type(exc).__name__
            report["elapsed_seconds"] = round(time.monotonic()-started, 3)
            args.report.parent.mkdir(parents=True, exist_ok=True)
            args.report.write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
            print(json.dumps(report, ensure_ascii=False))
    return 0 if report.get("passed") else 1


if __name__ == "__main__":
    raise SystemExit(main())

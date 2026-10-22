"""One explicitly authorized Argon stream while changing local Business settings.

Uses the existing model key only at the exact loopback origin; never prints it.
Restores test metadata with revision checks, without rotating any credentials.
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
        raise SystemExit("Reference model API key missing")
    report = {"origin": ORIGIN, "model": MODEL, "generation_calls": 0,
              "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "checks": {}}
    started = time.monotonic()
    pending_config = pending_account = original_config = original_account = None

    def guard(request):
        if (request.url.scheme, request.url.host, request.url.port) != ("http", "127.0.0.1", 2048):
            raise RuntimeError("Request left the selected loopback origin")

    with httpx.Client(base_url=ORIGIN, trust_env=False, follow_redirects=False,
                      timeout=httpx.Timeout(310, connect=10), event_hooks={"request": [guard]}) as http:
        def request(method, path, **kwargs):
            response = http.request(method, path, **kwargs)
            if response.status_code != 200:
                raise RuntimeError(f"Local operation returned HTTP {response.status_code}")
            return response.json()

        def config_equal(a, b):
            return {k: v for k, v in a.items() if k != "revision"} == {k: v for k, v in b.items() if k != "revision"}

        def restore_config():
            nonlocal pending_config
            if pending_config is None:
                return
            current = request("GET", "/api/addons/business/config")
            if current["revision"] != pending_config["revision"] or not config_equal(current, pending_config):
                raise RuntimeError("Concurrent configuration edit prevents automatic restoration")
            restored = copy.deepcopy(original_config)
            restored["revision"] = current["revision"]
            request("PUT", "/api/addons/business/config", json=restored)
            pending_config = None

        def restore_account():
            nonlocal pending_account
            if pending_account is None:
                return
            current = request("GET", "/api/addons/business/accounts")
            if current["revision"] != pending_account["revision"] or current["accounts"] != pending_account["accounts"]:
                raise RuntimeError("Concurrent account edit prevents automatic restoration")
            request("POST", "/api/addons/business/accounts", json={
                "revision": current["revision"], "id": original_account["id"],
                "label": original_account["label"], "enabled": original_account["enabled"]})
            pending_account = None

        def model_state():
            status = request("GET", "/api/addons/business/status")
            selected = next(m for m in status["model_states"] if m["public_id"] == MODEL)
            return {k: v for k, v in selected.items() if k != "eligible_account_ids"}, status["capacity"]

        try:
            original_config = request("GET", "/api/addons/business/config")
            accounts = request("GET", "/api/addons/business/accounts")
            enabled = [a for a in accounts["accounts"] if a["enabled"]]
            if len(enabled) != 1 or not original_config["enabled"]:
                raise RuntimeError("This bounded check requires the existing single-account local setup")
            original_account = enabled[0]
            before, _ = model_state()
            if not before["configuration_valid"] or not before["schedulable"]:
                raise RuntimeError("Argon is not currently schedulable")
            report["before"] = before
            chunks, times, text_times, text, finish = 0, [], [], [], None
            updated = False
            report["generation_calls"] = 1
            with http.stream("POST", f"/v1beta/models/{MODEL}:streamGenerateContent?alt=sse",
                headers={"x-goog-api-key": key, "X-AIStudio-Provider": "business"},
                json={"contents": [{"parts": [{"text": "Start with HOT-UPDATE-BEGIN. Write 72 numbered observations about rain, each a complete sentence with at least 12 words. End with HOT-UPDATE-END."}]}]}) as stream:
                if stream.status_code != 200:
                    raise RuntimeError(f"Generation returned HTTP {stream.status_code}")
                for line in stream.iter_lines():
                    if not line.startswith("data: "):
                        continue
                    event = json.loads(line[6:])
                    if "error" in event:
                        raise RuntimeError("Stream returned a native error")
                    chunks += 1
                    times.append(round(time.monotonic() - started, 3))
                    value = "".join(p.get("text", "") for c in event.get("candidates", [])
                                    for p in c.get("content", {}).get("parts", []) if not p.get("thought"))
                    if value:
                        text.append(value)
                        text_times.append(times[-1])
                    for candidate in event.get("candidates", []):
                        finish = candidate.get("finishReason") or finish
                    if value and not updated:
                        updated = True
                        busy, capacity = model_state()
                        report["during"] = {"model": busy, "capacity": capacity}
                        report["checks"]["busy_not_schedulable"] = not busy["schedulable"] and capacity["active"] == 1
                        pending_account = request("POST", "/api/addons/business/accounts", json={
                            "revision": accounts["revision"], "id": original_account["id"],
                            "label": "Phase 1 live label check", "enabled": True})
                        saved = next(a for a in pending_account["accounts"] if a["id"] == original_account["id"])
                        report["checks"]["label_preserves_credential_epoch"] = saved["epoch"] == original_account["epoch"]
                        candidate = copy.deepcopy(original_config)
                        candidate["max_concurrent"] = 2
                        binding = next(b for b in candidate["bindings"] if b["public_id"] == MODEL)
                        binding["display_name"] = "Argon phase 1 live metadata check"
                        binding["account_ids"] = ["acct-" + "0" * 32]
                        pending_config = request("PUT", "/api/addons/business/config", json=candidate)
                        scoped, capacity = model_state()
                        report["unavailable_scope"] = {"model": scoped, "capacity": capacity}
                        report["checks"]["unavailable_binding_not_ready"] = scoped["configuration_valid"] and not scoped["schedulable"] and not scoped["auth_verified"] and not scoped["model_verified"]
                        report["checks"]["concurrency_update_visible"] = capacity["limit"] == 2
                        restore_config()
                        restore_account()
            result = "".join(text)
            after, capacity = model_state()
            report["after"] = {"model": after, "capacity": capacity}
            report.update(chunks=chunks, text_chunks=len(text_times), finish_reason=finish,
                          event_seconds=times, output_bytes=len(result.encode()),
                          first_text_seconds=text_times[0] if text_times else None,
                          last_text_seconds=text_times[-1] if text_times else None)
            report["checks"]["stream_survived_saves"] = updated and finish == "STOP" and "HOT-UPDATE-BEGIN" in result and "HOT-UPDATE-END" in result
            report["checks"]["incremental_arrival"] = len(text_times) > 1 and text_times[-1] - text_times[0] >= 0.1
            report["checks"]["runtime_evidence"] = after["schedulable"] and after["auth_verified"] and after["model_verified"] and capacity["active"] == 0
            final_config = request("GET", "/api/addons/business/config")
            final_accounts = request("GET", "/api/addons/business/accounts")
            final_account = next(a for a in final_accounts["accounts"] if a["id"] == original_account["id"])
            report["checks"]["settings_restored"] = config_equal(final_config, original_config) and final_account == original_account
            report["final_config_revision"] = final_config["revision"]
            report["passed"] = all(report["checks"].values())
        except Exception as exc:
            report["passed"] = False
            report["error_type"] = type(exc).__name__
        finally:
            for name, restore in (("config", restore_config), ("account", restore_account)):
                try:
                    restore()
                except Exception as exc:
                    report["passed"] = False
                    report[f"{name}_restore_error_type"] = type(exc).__name__
            report["elapsed_seconds"] = round(time.monotonic() - started, 3)
            args.report.parent.mkdir(parents=True, exist_ok=True)
            args.report.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
            print(json.dumps(report, ensure_ascii=False))
    return 0 if report.get("passed") else 1


if __name__ == "__main__":
    raise SystemExit(main())

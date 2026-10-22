"""Explicit, bounded live checks against the user's loopback Business instance.

Requires google-genai==2.32.0. Credentials are read in memory from the existing
pool and sent only to loopback; this script never contacts Google directly.
The running Go adapter performs the authorized upstream calls. No retries.
"""
from __future__ import annotations

import argparse
import hashlib
import importlib.metadata
import json
from pathlib import Path
import sys
import time
from urllib.parse import urlsplit

import httpx

MODEL = "business-gemini-4-argon"
PROVIDER = {"X-AIStudio-Provider": "business"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["bootstrap", "unary", "sse", "files", "preflight"])
    parser.add_argument("--session", type=Path, required=True)
    parser.add_argument("--origin", default="http://127.0.0.1:2048")
    parser.add_argument("--fixtures", type=Path)
    parser.add_argument("--report", type=Path, required=True)
    args = parser.parse_args()
    origin = urlsplit(args.origin)
    if origin.scheme != "http" or origin.hostname != "127.0.0.1" or origin.path or origin.query or origin.fragment or origin.username:
        raise SystemExit("Only an exact IPv4 loopback HTTP origin is allowed")
    session = json.loads(args.session.read_text(encoding="utf-8-sig"))
    key = session["api_tokens"][0]
    if not isinstance(key, str) or not key:
        raise SystemExit("Reference model API key missing")
    report = {"action": args.action, "origin": args.origin, "model": MODEL,
              "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "checks": []}
    started = time.monotonic()
    def exact_origin(request):
        target = urlsplit(str(request.url))
        if (target.scheme, target.hostname, target.port) != (origin.scheme, origin.hostname, origin.port):
            raise RuntimeError("A request attempted to leave the selected loopback origin")

    http = httpx.Client(base_url=args.origin, trust_env=False, follow_redirects=False,
                        timeout=httpx.Timeout(310, connect=10), event_hooks={"request": [exact_origin]})

    def request(method, path, **kwargs):
        response = http.request(method, path, **kwargs)
        if not response.is_success:
            # Do not expose credential bodies, response headers, or generic errors.
            raise RuntimeError(f"Local endpoint {path.split('?')[0]} returned HTTP {response.status_code}")
        return response.json()

    def text_of(response):
        return "".join(p.text or "" for c in response.candidates or []
                       for p in c.content.parts or [] if not p.thought)

    def receipt(response, **extra):
        value = text_of(response)
        return {"text": value, "text_bytes": len(value.encode()),
                "finish_reason": str(response.candidates[0].finish_reason.value),
                "usage_present": response.usage_metadata is not None, **extra}

    try:
        if importlib.metadata.version("google-genai") != "2.32.0":
            raise RuntimeError("This acceptance script requires google-genai==2.32.0")
        if args.action == "bootstrap":
            accounts = request("GET", "/api/addons/business/accounts")
            if not accounts["accounts"]:
                available = [a for a in session["accounts"] if a.get("available", True)]
                if len(available) != 1:
                    raise RuntimeError("Expected exactly one available reference account")
                a = available[0]
                credential = {k: str(a.get(k, "")) for k in ["secure_c_ses", "host_c_oses", "csesidx", "user_agent"]}
                credential["config_id"] = a["team_id"]
                accounts = request("POST", "/api/addons/business/accounts", json={
                    "revision": accounts["revision"], "label": "Existing Business account",
                    "enabled": True, "credential": credential})
                credential.clear()
            config = request("GET", "/api/addons/business/config")
            if sum(b["public_id"] == MODEL for b in config["bindings"]) != 1:
                raise RuntimeError("Expected exactly one Argon model binding")
            if not any(a["enabled"] for a in accounts["accounts"]):
                raise RuntimeError("No enabled Business account is available")
            for binding in config["bindings"]:
                binding["enabled"] = binding["public_id"] == MODEL
                if binding["enabled"]:
                    binding.update(selector_mode="exact", upstream_model_id="gemini-4-argon",
                        verification_status="verified", evidence="Existing pool exact Argon mapping and sanitized successful live report, 2026-10-22",
                        account_ids=[a["id"] for a in accounts["accounts"] if a["enabled"]])
            config.update(enabled=True, max_concurrent=1, request_timeout_seconds=300)
            request("PUT", "/api/addons/business/config", json=config)
            status = request("GET", "/api/addons/business/status")
            report["checks"].append({"enabled_accounts": sum(a["enabled"] for a in accounts["accounts"]),
                "model_binding": MODEL, "status": status,
                "passed": status.get("state") == "ready" and status.get("generation_ready") is True})
        elif args.action == "preflight":
            for field, value in {"temperature": 0, "maxOutputTokens": 64,
                                 "topP": 0.5, "thinkingConfig": {"includeThoughts": False}}.items():
                response = http.post(f"/v1beta/models/{MODEL}:generateContent",
                    headers={**PROVIDER, "x-goog-api-key": key}, json={
                        "contents": [{"parts": [{"text": "Reply OK."}]}], "generationConfig": {field: value}})
                body = response.json()
                error = body.get("error", {})
                report["checks"].append({"field": field, "http_status": response.status_code,
                    "body": body, "passed": response.status_code == 400 and error.get("status") == "INVALID_ARGUMENT"
                        and error.get("message") == "Business does not have a verified mapping for generationConfig." + field})
        else:
            from google import genai
            from google.genai import types
            sdk = genai.Client(api_key=key, vertexai=False, http_options=types.HttpOptions(
                base_url=args.origin, api_version="v1beta", headers=PROVIDER,
                httpx_client=http, retry_options=types.HttpRetryOptions(attempts=1)))
            if args.action == "unary":
                model = sdk.models.get(model=MODEL)
                response = sdk.models.generate_content(model=MODEL,
                    contents='Return exactly this JSON, without markdown: {"probe":"ARGON-NATIVE-20261022","sum":42}')
                check = receipt(response, supported_actions=model.supported_actions)
                try:
                    parsed = json.loads(check["text"])
                except ValueError:
                    parsed = None
                check["passed"] = parsed == {"probe": "ARGON-NATIVE-20261022", "sum": 42} and check["finish_reason"] == "STOP"
                report["checks"].append(check)
            elif args.action == "sse":
                chunks, text_chunks, times, text_times, texts, finish = 0, 0, [], [], [], None
                for response in sdk.models.generate_content_stream(model=MODEL,
                    contents="Write exactly 24 numbered one-sentence observations about rain, numbered 1 through 24. Start with STREAM-BEGIN and end with STREAM-END."):
                    chunks += 1
                    times.append(round(time.monotonic() - started, 3))
                    value = text_of(response)
                    if value:
                        text_chunks += 1
                        text_times.append(times[-1])
                        texts.append(value)
                    if response.candidates and response.candidates[0].finish_reason:
                        finish = response.candidates[0].finish_reason.value
                value = "".join(texts)
                span = round(text_times[-1] - text_times[0], 3) if text_times else 0
                report["checks"].append({"chunks": chunks, "text_chunks": text_chunks,
                    "first_event_seconds": times[0] if times else None, "last_event_seconds": times[-1] if times else None,
                    "event_times_seconds": times, "text_arrival_span_seconds": span,
                    "incremental_arrival_observed": span >= 0.1,
                    "finish_reason": finish, "text": value,
                    "passed": text_chunks > 1 and span >= 0.1 and finish == "STOP" and "STREAM-BEGIN" in value and "STREAM-END" in value})
            else:
                if not args.fixtures:
                    raise RuntimeError("--fixtures expected.json is required")
                fixture = json.loads(args.fixtures.read_text(encoding="utf-8-sig"))
                # Fixture contract is deliberately simple and contains only synthetic data.
                for item in fixture["fixtures"]:
                    path = Path(item["path"])
                    if hashlib.sha256(path.read_bytes()).hexdigest() != item["sha256"]:
                        raise RuntimeError("Synthetic file fixture hash changed")
                    uploaded = sdk.files.upload(file=path, config=types.UploadFileConfig(mime_type=item["mime_type"], display_name=path.name))
                    try:
                        response = sdk.models.generate_content(model=MODEL, contents=[
                            types.Part.from_uri(file_uri=uploaded.uri, mime_type=item["mime_type"]), item["prompt"]])
                        check = receipt(response, mime_type=item["mime_type"], input_bytes=path.stat().st_size,
                                        input_sha256=hashlib.sha256(path.read_bytes()).hexdigest())
                        expected = item["expected"]
                        check["expected"] = expected
                        try:
                            parsed = json.loads(check["text"])
                        except ValueError:
                            parsed = None
                        check["passed"] = parsed == expected and check["finish_reason"] == "STOP"
                        report["checks"].append(check)
                    finally:
                        sdk.files.delete(name=uploaded.name)
                    deleted = http.get("/v1beta/" + uploaded.name, headers={**PROVIDER, "x-goog-api-key": key})
                    report["checks"][-1]["deleted_http_status"] = deleted.status_code
                    report["checks"][-1]["passed"] &= deleted.status_code == 404
            sdk.close()
        report["passed"] = bool(report["checks"]) and all(c.get("passed") is True for c in report["checks"])
    except Exception as exc:
        # Exception class is safe; SDK errors can include upstream/body context.
        report["passed"] = False
        report["error_type"] = type(exc).__name__
        if isinstance(exc, RuntimeError):
            report["error"] = str(exc)
        if hasattr(exc, "code"):
            report["http_status"] = exc.code
        try:
            report["local_status"] = request("GET", "/api/addons/business/status")
        except Exception:
            report["local_status"] = "unreachable"
    finally:
        http.close()
        report["elapsed_seconds"] = round(time.monotonic() - started, 3)
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
        print(json.dumps(report, ensure_ascii=False))
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())

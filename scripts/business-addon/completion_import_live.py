"""Exercise local model-config preview/CAS/undo with a disabled synthetic entry.

No account files, model calls, secrets, external URLs or automatic retries.
"""
from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import secrets
import urllib.error
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def digest(config: dict) -> str:
    value = {key: val for key, val in config.items() if key != "revision"}
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--allow-local-config-changes", action="store_true")
    parser.add_argument("--report", type=Path, required=True)
    args = parser.parse_args()
    if not args.allow_local_config_changes:
        parser.error("Explicit --allow-local-config-changes is required")
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    report = {"scope": "local synthetic disabled model import and undo", "model_calls": 0, "checks": {}, "requests": []}
    checks = report["checks"]
    root = "/api/addons/business"
    allowed = {root + suffix for suffix in ("/config", "/status", "/config/import", "/config/import/preview", "/config/import/rollback")}

    def request(path: str, body: dict | None = None, expected: int = 200) -> dict:
        assert path in allowed
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request("http://127.0.0.1:2048" + path, data=data,
                                     headers={"Content-Type": "application/json"} if data else {})
        try:
            response = opener.open(req, timeout=15)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            status = response.status
            raw = response.read(256 * 1024 + 1)
        report["requests"].append({"path": path, "method": req.get_method(), "status": status})
        assert status == expected and len(raw) <= 256 * 1024, "unexpected_local_response"
        return json.loads(raw)

    undo = None
    before = None
    try:
        state = request(root + "/status")
        assert state["capacity"]["active"] == 0 and state["resources"]["in_use_bytes"] == 0
        before = request(root + "/config")
        revision = before["revision"]
        source = {"models": [{"id": "completion-fixture-" + secrets.token_hex(6), "name": "Synthetic import regression", "routing_mode": "explicit", "upstream_model_id": "synthetic-unused", "enabled": True}]}
        preview = request(root + "/config/import/preview", {"revision": revision, "source": source})
        after_preview = request(root + "/config")
        checks["preview_no_write"] = after_preview == before
        checks["preview_disabled_unverified"] = len(preview["added"]) == 1 and not preview["added"][0]["enabled"] and preview["added"][0]["verification_status"] == "unverified"
        request(root + "/config/import", {"revision": revision, "source_hash": "changed", "source": source}, 409)
        checks["source_hash_change_rejected"] = request(root + "/config") == before
        result = request(root + "/config/import", {"revision": revision, "source_hash": preview["source_hash"], "source": source})
        undo = {"revision": result["config"]["revision"], "rollback_id": result["rollback_id"]}
        applied = request(root + "/config")
        original_bindings = applied["bindings"][:-1]
        checks["only_disabled_model_appended"] = (original_bindings == before["bindings"] and len(applied["bindings"]) == len(before["bindings"]) + 1 and not applied["bindings"][-1]["enabled"] and applied["revision"] == revision + 1)
        checks["other_settings_unchanged"] = all(applied[key] == value for key, value in before.items() if key not in ("revision", "bindings"))
        request(root + "/config/import", {"revision": revision, "source_hash": preview["source_hash"], "source": source}, 409)
        request(root + "/config/import/rollback", {**undo, "revision": revision}, 409)
        checks["stale_revision_rejected"] = True
        restored = request(root + "/config/import/rollback", undo)
        used_undo = undo
        undo = None
        checks["undo_restored_settings"] = digest(restored) == digest(before) and restored["revision"] == revision + 2
        request(root + "/config/import/rollback", {**used_undo, "revision": restored["revision"]}, 409)
        request(root + "/config/import/preview", {"revision": restored["revision"], "source": {"models": source["models"], "accounts": []}}, 400)
        checks["replayed_undo_and_account_document_rejected"] = True
        final = request(root + "/config")
        report["initial_revision"], report["final_revision"] = revision, final["revision"]
        report["initial_settings_sha256"], report["final_settings_sha256"] = digest(before), digest(final)
        checks["final_settings_restored"] = digest(final) == digest(before)
    except Exception as error:
        report["error_type"] = type(error).__name__
    finally:
        if undo is not None:
            try:
                current = request(root + "/config")
                if current["revision"] == undo["revision"]:
                    restored = request(root + "/config/import/rollback", undo)
                    report["cleanup_restored"] = digest(restored) == digest(before)
                else:
                    report["cleanup_restored"] = False
            except Exception as error:
                report["cleanup_error_type"] = type(error).__name__
        report["passed"] = "error_type" not in report and len(checks) == 9 and all(checks.values())
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
        print(json.dumps(report))
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())

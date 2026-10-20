"""G6 real-handler acceptance and reusable synthetic fixture process driver.

No fake HTTP responses: every client request goes to the Go fixture executable.
--check performs source/schema checks only and never claims HTTP acceptance.
"""
from __future__ import annotations

import argparse
import ast
from datetime import datetime, timezone
import importlib
import json
import os
from pathlib import Path
import queue
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from urllib.error import HTTPError
from urllib.parse import urlsplit
from urllib.request import HTTPRedirectHandler, ProxyHandler, Request, build_opener

sys.dont_write_bytecode = True
PROJECT = Path(__file__).resolve().parents[1]
TERMINAL = {"succeeded", "partial", "failed", "cancelled", "interrupted"}


def synthetic_credentials(label="one", revision=1):
    return {
        "accountName": f"g6-{label}@example.invalid", "origins": [],
        "cookies": [{"name": name, "value": f"g6-synthetic-{label}-{revision}-{name}",
                     "domain": ".google.com", "path": "/", "expires": -1,
                     "httpOnly": True, "secure": True, "sameSite": "None"}
                    for name in ("SAPISID", "__Secure-1PAPISID", "__Secure-3PAPISID")],
    }


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class FixtureProcess:
    """M4 reusable API: start(), wire(), poll(), metrics(), stop(), restart().

    Creates only its own OS-temp root. Keys are synthetic process-local inputs;
    callers must not copy the ready record into a persistent report.
    """
    def __init__(self, executable: Path | str):
        self.executable = Path(executable).resolve()
        self.root = Path(tempfile.mkdtemp(prefix="aistudio2api-g6-"))
        # Avoid a top-level "secrets" import: Manager's scripts/secrets.py can
        # shadow that stdlib name when this driver is imported from its runner.
        self.nonce = os.urandom(16).hex()
        (self.root / ".g6-fixture").write_text(self.nonce, encoding="ascii")
        self.process = None
        self.ready = None
        self.calls = []
        self._opener = build_opener(ProxyHandler({}), NoRedirect())

    def start(self, timeout=30):
        if self.process is not None:
            raise RuntimeError("fixture already started")
        env = dict(os.environ, G6_MANAGEMENT_FIXTURE="1",
                   G6_MANAGEMENT_ROOT=str(self.root), G6_MANAGEMENT_NONCE=self.nonce)
        # Same OS identity and policy as ordinary test execution. No shell,
        # allowlist change, relocation workaround or alternate execution host.
        self.process = subprocess.Popen(
            [str(self.executable), "-test.run=^TestManagementAcceptanceFixtureProcess$",
             "-test.v", "-test.timeout=16m"], env=env, stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT, text=True, encoding="utf-8", errors="replace",
            creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
        )
        lines = queue.Queue()
        self._lines = lines

        def read_lines():
            for line in self.process.stdout:
                # Fixture output is not persisted; the ready line has synthetic
                # keys and must not flow into a report even on startup failure.
                lines.put(line)
            lines.put(None)

        threading.Thread(target=read_lines, daemon=True).start()
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                line = lines.get(timeout=min(1, max(.01, deadline-time.monotonic())))
            except queue.Empty:
                continue
            if line is None:
                raise RuntimeError(f"fixture exited before ready (exit={self.process.poll()})")
            if line.startswith("G6_READY "):
                self.ready = json.loads(line[len("G6_READY "):])
                origin = urlsplit(self.ready["origin"])
                if (self.ready.get("protocol") != "g6-fixture-v1" or
                        self.ready.get("syntheticOnly") is not True or
                        origin.scheme != "http" or origin.hostname != "127.0.0.1" or
                        not origin.port or origin.username or origin.password or origin.path):
                    raise RuntimeError("invalid fixture ready record")
                return self
        raise RuntimeError("fixture startup timed out")

    @property
    def origin(self):
        return self.ready["origin"]

    def diagnostics(self):
        lines=[]
        channel=getattr(self,"_lines",None)
        if channel is not None:
            while True:
                try:
                    line=channel.get_nowait()
                except queue.Empty:
                    break
                if line and not line.startswith("G6_READY "):
                    lines.append(line.rstrip())
        return lines[-30:]

    def token(self, template="admin"):
        return self.ready["keys"][template]["token"]

    def wire(self, method, path, body=None, *, template="admin", idem=None,
             headers=None, raw=None, native=False):
        if not path.startswith("/") or path.startswith("//") or "#" in path:
            raise ValueError("fixture path required")
        actual_headers = {"Accept": "application/json"}
        if template is not None:
            actual_headers["Authorization"] = "Bearer " + self.token(template)
        if idem is not None:
            actual_headers["Idempotency-Key"] = idem
        if body is not None:
            raw = json.dumps(body, separators=(",", ":")).encode()
        if raw is not None:
            actual_headers["Content-Type"] = "application/json"
        actual_headers.update(headers or {})
        request = Request(self.origin+path, data=raw, headers=actual_headers, method=method)
        self.calls.append((method, path))
        try:
            response = self._opener.open(request, timeout=15)
        except HTTPError as error:
            response = error
        except OSError:
            diagnostic=self.diagnostics()
            if diagnostic:
                print("Fixture diagnostic:\n"+"\n".join(diagnostic),file=sys.stderr)
            raise
        with response:
            status, result_headers = response.code, response.headers
            payload = json.loads(response.read())
        if not native:
            if (result_headers.get("Content-Type", "").split(";")[0] != "application/json" or
                    result_headers.get("Cache-Control") != "no-store" or
                    result_headers.get("X-Request-Id") != payload.get("requestId")):
                raise AssertionError("management response headers/envelope mismatch")
            import re
            if not re.fullmatch(r"req_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}", payload.get("requestId", "")):
                raise AssertionError("management request ID invalid")
            expected = {"error", "requestId"} if status >= 400 else {"data", "requestId"}
            if set(payload) != expected:
                raise AssertionError("management envelope has unexpected fields")
        return status, result_headers, payload

    def metrics(self):
        return self.wire("GET", "/__g6__/state", native=True)[2]

    def barrier(self, point):
        status, _, _ = self.wire("POST", "/__g6__/barrier", {"point": point}, native=True)
        if status != 200:
            raise AssertionError("fixture barrier was not armed")

    def wait_barrier(self, point, timeout=10):
        deadline=time.monotonic()+timeout
        while time.monotonic()<deadline:
            if self.metrics()["barrierReached"]==point:
                return
            time.sleep(.03)
        raise AssertionError("real participant did not reach fixture barrier")

    def release(self):
        self.wire("POST", "/__g6__/release", {}, native=True)

    def auth_expired(self, account_id):
        status,_,payload=self.wire("POST","/__g6__/auth-expired",{"accountId":account_id},native=True)
        if status!=200:
            raise AssertionError("synthetic auth-expired commit rejected")
        return payload["account"]

    def native_restore(self, account_id):
        status,_,payload=self.wire("POST","/__g6__/native-restore",{"accountId":account_id},native=True)
        if status!=200:
            raise AssertionError("synthetic native restore rejected")
        return payload["account"]

    def claim_fault(self, mode, count=1):
        status,_,_=self.wire("POST","/__g6__/claim-fault",{"mode":mode,"count":count},native=True)
        if status!=200:
            raise AssertionError("synthetic ClaimTask fault was not armed")

    def poll(self, task_id, timeout=15):
        deadline = time.monotonic()+timeout
        while time.monotonic() < deadline:
            status, _, payload = self.wire("GET", "/api/manage/v1/tasks/"+task_id)
            if status != 200:
                raise AssertionError("task read failed")
            if payload["data"]["status"] in TERMINAL:
                return payload["data"]
            time.sleep(.03)
        raise AssertionError("task did not reach a terminal state")

    def stop(self, *, crash=False):
        if self.process is None:
            return
        process = self.process
        try:
            if process.poll() is None:
                if crash or self.ready is None:
                    process.kill()
                else:
                    self.wire("POST", "/__g6__/stop", body={}, native=True)
                process.wait(timeout=10)
            if not crash and self.ready is not None and process.returncode != 0:
                raise RuntimeError(f"fixture cleanup failed (exit={process.returncode})")
        finally:
            if process.poll() is None:
                process.kill()
                process.wait(timeout=5)
            if process.stdout:
                process.stdout.close()
            self.process, self.ready = None, None

    def restart(self, *, crash=False):
        self.stop(crash=crash)
        return self.start()

    def close(self):
        try:
            self.stop()
        finally:
            # Delete only the explicitly created OS-temp root after marker check.
            import shutil
            if ((self.root / ".g6-fixture").read_text(encoding="ascii") != self.nonce or
                    self.root.resolve().parent != Path(tempfile.gettempdir()).resolve() or
                    not self.root.name.startswith("aistudio2api-g6-")):
                raise RuntimeError("refusing unsafe fixture cleanup")
            shutil.rmtree(self.root)


class RecordingSession:
    """Real requests transport with a deliberate post-response lost-ack fault."""
    def __init__(self, requests_module):
        self.delegate = requests_module.Session()
        self.delegate.trust_env = False
        self.calls = []
        self.drop_next_write = False

    @property
    def trust_env(self):
        return self.delegate.trust_env

    @trust_env.setter
    def trust_env(self, value):
        self.delegate.trust_env = value

    def request(self, method, url, **kwargs):
        self.calls.append((method, urlsplit(url).path))
        response = self.delegate.request(method, url, **kwargs)
        if self.drop_next_write and method not in {"GET", "HEAD"}:
            self.drop_next_write = False
            response.close()
            raise ConnectionError("synthetic accepted response lost")
        return response

    def close(self):
        self.delegate.close()


class RealHandlerAcceptance(unittest.TestCase):
    executable = None
    manager_root = None

    @classmethod
    def setUpClass(cls):
        sys.path.insert(0, str(cls.manager_root))
        cls.client_module = importlib.import_module("enterprise.aitoapi_client")
        cls.readback_module = importlib.import_module("enterprise.aitoapi_readback")
        cls.requests_module = importlib.import_module("requests")

    def setUp(self):
        self.fixture = FixtureProcess(self.executable)
        self.addCleanup(self.fixture.close)
        self.fixture.start()
        self.session = RecordingSession(self.requests_module)
        self.client = self.client_module.AitoapiManagementClient(
            self.fixture.origin, self.fixture.token(), session=self.session, allow_loopback=True)
        self.addCleanup(self.client.close)

    def error(self, method, path, code, status, **kwargs):
        actual, headers, payload = self.fixture.wire(method, path, **kwargs)
        self.assertEqual(actual, status)
        self.assertEqual(payload["error"], {"code": code, "message": self.client_module.REMOTE_ERRORS.get(code, (None, "A console-password session is required."))[1]})
        return headers

    def imported(self, label="lifecycle"):
        credentials = synthetic_credentials(label)
        ack = self.client.submit_import([{"clientRef": "g6-"+label, "credentials": credentials}], "g6-ok", "g6-import-"+label)
        self.assertEqual(ack["status"], "queued")
        task = self.fixture.poll(ack["taskId"])
        self.assertEqual(task["status"], "succeeded")
        item = task["items"][0]
        self.assertEqual(item["upload"]["status"], "committed")
        self.assertEqual(item["result"]["stage"], "uploaded")
        return credentials, ack, task, self.client.account(item["accountId"])

    def test_empty_preflight_and_strict_errors(self):
        self.assertEqual(self.client.accounts_page()["items"], [])
        self.assertEqual(self.client.tasks_page()["items"], [])
        self.assertIs(self.client._request("GET", "/api/manage/v1/system/readiness")["ready"], False)
        self.assertTrue(self.client.preflight()["accounts_write_admission"])
        self.assertEqual(self.client.tasks_page()["total"], 0)
        for path in ("/api/manage/v1", "/api/manage/v1/missing", "/api/manage/v1//missing", "/api/manage/v1/accounts/../missing"):
            self.error("GET", path, "NOT_FOUND", 404)
        headers = self.error("DELETE", "/api/manage/v1/accounts", "METHOD_NOT_ALLOWED", 405)
        self.assertIn("GET", headers.get("Allow", ""))
        for query in ("limit=0", "limit=201", "offset=-1", "limit=nan"):
            self.error("GET", "/api/manage/v1/accounts?"+query, "INVALID_REQUEST", 400)
        self.error("GET", "/api/manage/v1/accounts", "UNAUTHORIZED", 401, template=None)
        self.error("POST", "/api/manage/v1/accounts/import", "FORBIDDEN", 403, template="readonly", body={"items": []})
        for raw in (b'{"items":[],"verify":false,"verify":true}', b'{"items":[],"unknown":true}', b'{"items":null}', b'{'):
            self.error("POST", "/api/manage/v1/accounts/import", "INVALID_REQUEST", 400, raw=raw)
        self.assertEqual(self.fixture.metrics()["generated"], 0)

    def test_import_replace_state_and_immutable_receipts(self):
        credentials, ack, original, account = self.imported()
        receipt = original["items"][0]["upload"]
        replay = self.client.submit_import([{"clientRef": "g6-lifecycle", "credentials": credentials}], "g6-ok", "g6-import-lifecycle")
        self.assertEqual(replay, ack)
        with self.assertRaises(self.client_module.AitoapiClientError) as caught:
            self.client.submit_import([{"clientRef": "g6-other", "credentials": credentials}], "g6-ok", "g6-import-lifecycle")
        self.assertEqual(caught.exception.code, "IDEMPOTENCY_CONFLICT")
        self.assertTrue(caught.exception.valid_error_envelope)
        self.assertFalse(caught.exception.not_admitted)
        duplicate = self.client.submit_import([{"clientRef":"g6-duplicate","credentials":credentials}], "g6-ok", "g6-duplicate")
        duplicate_task = self.fixture.poll(duplicate["taskId"])
        self.assertEqual(duplicate_task["items"][0]["error"]["code"], "DUPLICATE_ACCOUNT")
        old_id, old_index = account["accountId"], account["index"]
        disabled = self.client.disable_if_version(old_id, account["credentialVersion"], account["stateVersion"])
        self.assertIs(disabled["enabled"], False)
        self.error("PATCH", "/api/manage/v1/accounts/"+old_id, "VERSION_CONFLICT", 409,
                   body={"enabled":True,"expectedCredentialVersion":account["credentialVersion"],"expectedStateVersion":account["stateVersion"]})
        enabled = self.client.enable_if_version(old_id, disabled["credentialVersion"], disabled["stateVersion"])
        self.assertIs(enabled["enabled"], True)
        archive = self.client._request("POST", f"/api/manage/v1/accounts/{old_id}/archive", body={}, idempotency_key="g6-archive")
        self.assertEqual(self.fixture.poll(archive["taskId"])["status"], "succeeded")
        self.assertIs(self.client.account(old_id)["archived"], True)
        restore = self.client.submit_restore(old_id, "g6-restore")
        self.assertEqual(self.fixture.poll(restore["taskId"])["status"], "succeeded")
        fresh = self.client.account(old_id)
        self.assertIs(fresh["archived"], False)
        replacement = self.client.submit_replace(old_id, synthetic_credentials("lifecycle", 2), "g6-replace",
            credential_version=fresh["credentialVersion"], state_version=fresh["stateVersion"])
        replaced = self.fixture.poll(replacement["taskId"])
        self.assertEqual(replaced["status"], "succeeded")
        current = self.client.account(old_id)
        self.assertEqual((current["accountId"], current["index"]), (old_id, old_index))
        self.assertGreater(current["credentialVersion"], receipt["credentialVersion"])
        self.assertEqual(self.client.task(ack["taskId"])["items"][0]["upload"], receipt)
        self.assertEqual(self.fixture.metrics()["generated"], 0)
        self.assertEqual(self.fixture.metrics()["admittedTasks"], 5)
        self.assertEqual(self.fixture.metrics()["mutations"]["import"], 1)
        self.assertEqual(self.fixture.metrics()["mutations"]["replace"], 1)

    def test_response_lost_uses_actual_client_readback_gets_only(self):
        self.session.drop_next_write = True
        submitted = datetime.now(timezone.utc)
        with self.assertRaises(self.client_module.AitoapiClientError) as caught:
            self.client.submit_import([{"clientRef":"g6-lost","credentials":synthetic_credentials("lost")}], "g6-ok", "g6-lost")
        self.assertEqual(caught.exception.code, "NETWORK_RESULT_UNKNOWN")
        deadline = time.monotonic()+10
        while self.client.tasks_page()["items"][0]["status"] not in TERMINAL and time.monotonic()<deadline:
            time.sleep(.03)
        checkpoint = {"server_origin":self.fixture.origin,"operation":"import","client_ref":"g6-lost",
                      "remote_management_key_id":self.fixture.ready["keys"]["admin"]["keyId"],
                      "submitted_at":submitted.isoformat()}
        before = len(self.session.calls)
        result = self.readback_module.AitoapiTaskReadback().inspect(
            {"checkpoint":checkpoint}, server={"origin":self.fixture.origin}, email="g6-lost@example.invalid", client=self.client)
        self.assertEqual(result["finding"], "upload_committed")
        self.assertTrue(self.session.calls[before:])
        self.assertTrue(all(method=="GET" for method,_ in self.session.calls[before:]))
        self.assertEqual(self.client.tasks_page()["total"], 1)

    def test_targeted_verifier_rejects_all_negative_observations(self):
        _,_,_,account = self.imported("verification")
        for model in ("g6-wrongacct","g6-buildonly","g6-empty","g6-thoughtonly","g6-finishonly",
                      "g6-missingterminal","g6-wrongmodel","g6-unconfirmed","g6-timeout"):
            with self.subTest(model=model):
                ack = self.client.submit_test(account["accountId"], model, "g6-test-"+model)
                task = self.fixture.poll(ack["taskId"])
                self.assertNotEqual(task["status"], "succeeded")
                self.assertFalse(any(item.get("result",{}).get("stage")=="model_verified" for item in task["items"]))
        ack = self.client.submit_test(account["accountId"], "g6-ok", "g6-test-ok")
        task = self.fixture.poll(ack["taskId"])
        self.assertEqual(task["status"], "succeeded")
        result = task["items"][0]["result"]
        self.assertEqual((result["stage"],result["actualChannel"]), ("model_verified","playground"))
        self.assertEqual(result["credentialVersion"],account["credentialVersion"])
        # The actual new adapter/readback must accept this Playground extension.
        new_client=importlib.import_module("enterprise.aistudio2api_client").AIStudio2APIManagementClient(
            self.fixture.origin,self.fixture.token(),allow_loopback=True)
        self.addCleanup(new_client.close)
        self.assertEqual(new_client.account(account["accountId"])["accountId"],account["accountId"])
        new_readback=importlib.import_module("enterprise.aistudio2api_readback").AIStudio2APITaskReadback()
        self.assertTrue(new_readback.verified_result(task,task["items"][0],account,"g6-ok"))
        self.assertEqual(new_client.capabilities()["backend"],"aistudio2api")
        self.assertIs(new_client.preflight()["readiness"]["ready"],False)

    def test_cancel_restart_never_repeats_generation_or_upload(self):
        _, ack, original, account = self.imported("restart")
        receipt = original["items"][0]["upload"]
        pending = self.client.submit_test(account["accountId"], "g6-cancel", "g6-cancel")
        deadline = time.monotonic()+5
        while self.fixture.metrics()["generated"] < 1 and time.monotonic()<deadline:
            time.sleep(.03)
        self.assertEqual(self.fixture.metrics()["generated"],1)
        self.client._request("POST",f"/api/manage/v1/tasks/{pending['taskId']}/cancel",body={})
        self.assertIn(self.fixture.poll(pending["taskId"])["status"], {"cancelled","failed"})
        self.assertEqual(self.fixture.metrics()["generated"],1)
        pending = self.client.submit_test(account["accountId"], "g6-cancel", "g6-crash")
        deadline = time.monotonic()+5
        while self.fixture.metrics()["generated"] < 2 and time.monotonic()<deadline:
            time.sleep(.03)
        self.assertEqual(self.fixture.metrics()["generated"],2)
        self.fixture.restart(crash=True)
        # Startup creates fresh synthetic keys; task attribution and prior
        # receipts remain in the real SQLite store. No write is resubmitted.
        self.client.close()
        self.session = RecordingSession(self.requests_module)
        self.client = self.client_module.AitoapiManagementClient(self.fixture.origin,self.fixture.token(),session=self.session,allow_loopback=True)
        self.addCleanup(self.client.close)
        interrupted = self.client.task(pending["taskId"])
        self.assertEqual(interrupted["status"],"interrupted")
        self.assertEqual(self.fixture.metrics()["generated"],0)
        self.assertEqual(self.client.task(ack["taskId"])["items"][0]["upload"], receipt)
        self.assertEqual(self.client.accounts_page()["total"],1)

    def reconnect(self, token=None):
        self.client.close()
        self.session=RecordingSession(self.requests_module)
        self.client=self.client_module.AitoapiManagementClient(self.fixture.origin,token or self.fixture.token(),session=self.session,allow_loopback=True)
        self.addCleanup(self.client.close)

    def test_crash_at_actual_file_and_receipt_boundaries(self):
        committed=0
        for point in ("before-prepare","after-prepare","after-apply","before-finalize","after-finalize"):
            with self.subTest(point=point):
                self.fixture.barrier(point)
                ack=self.client.submit_import([{"clientRef":"g6-"+point,"credentials":synthetic_credentials(point)}],"g6-ok","g6-crash-"+point)
                self.fixture.wait_barrier(point)
                self.assertEqual(self.fixture.metrics()["generated"],0)
                self.fixture.restart(crash=True)
                self.reconnect()
                task=self.client.task(ack["taskId"])
                self.assertEqual(task["status"],"interrupted")
                if point in {"before-finalize","after-finalize"}:
                    committed+=1
                    receipt=task["items"][0]["upload"]
                    self.assertEqual(receipt["status"],"committed")
                    account=self.client.account(receipt["accountId"])
                    self.assertEqual((receipt["credentialVersion"],receipt["stateVersion"]),
                                     (account["credentialVersion"],account["stateVersion"]))
                else:
                    self.assertFalse(task["items"][0].get("upload"))
                self.assertEqual(self.client.accounts_page()["total"],committed)
                self.assertEqual(self.fixture.metrics()["generated"],0)
                self.assertEqual(self.fixture.metrics()["mutations"],{})

    def test_console_key_auth_scopes_revocation_and_native_routes(self):
        self.error("GET","/api/management-keys","CONSOLE_PASSWORD_REQUIRED",403)
        self.error("GET","/api/manage/v1/accounts","UNAUTHORIZED",401,template=None,
                   headers={"Authorization":"Bearer g6-synthetic-model-key"})
        # Real native password session; synthetic-only values, no actual user config.
        status,headers,_=self.fixture.wire("POST","/api/auth/login",{"username":"g6-synthetic-console","password":"g6-synthetic-password"},template=None,native=True)
        self.assertEqual(status,200)
        cookie=headers.get("Set-Cookie").split(";",1)[0]
        console={"Cookie":cookie,"X-Requested-With":"XMLHttpRequest","Origin":self.fixture.origin}
        self.error("POST","/api/management-keys","FORBIDDEN",403,body={"name":"g6-test","scopes":["accounts:read"]},headers={**console,"Sec-Fetch-Site":"cross-site"})
        status,_,created=self.fixture.wire("POST","/api/management-keys",{"name":"g6-test-read","scopes":["accounts:read"]},headers=console)
        self.assertEqual(status,200)
        key=created["data"]
        self.error("GET","/api/manage/v1/tasks","FORBIDDEN",403,template=None,headers={"Authorization":"Bearer "+key["token"]})
        status,_,_=self.fixture.wire("GET","/api/manage/v1/accounts",template=None,headers={"Authorization":"Bearer "+key["token"]})
        self.assertEqual(status,200)
        status,_,revoked=self.fixture.wire("DELETE","/api/management-keys/"+key["key"]["id"],{},headers=console)
        self.assertEqual(status,200)
        self.assertIs(revoked["data"]["revoked"],True)
        self.error("GET","/api/manage/v1/accounts","UNAUTHORIZED",401,template=None,headers={"Authorization":"Bearer "+key["token"]})
        status,_,payload=self.fixture.wire("GET","/health",template=None,native=True)
        self.assertEqual((status,payload),(200,{"status":"ok"}))
        status,_,_=self.fixture.wire("GET","/v1/models",template=None,native=True)
        self.assertEqual(status,401)
        status,_,models=self.fixture.wire("GET","/v1/models",template=None,native=True,headers={"Authorization":"Bearer g6-synthetic-model-key"})
        self.assertEqual(status,200)
        self.assertEqual(models["data"],[])
        self.assertEqual(self.fixture.metrics()["generated"],0)

    def test_key_revocation_cancels_queued_uncommitted_work(self):
        _,_,_,account=self.imported("revoke")
        running=self.client.submit_test(account["accountId"],"g6-cancel","g6-revoke-running")
        deadline=time.monotonic()+5
        while self.fixture.metrics()["generated"]<1 and time.monotonic()<deadline:
            time.sleep(.03)
        self.assertEqual(self.fixture.metrics()["generated"],1)
        _,headers,_=self.fixture.wire("POST","/api/auth/login",{"username":"g6-synthetic-console","password":"g6-synthetic-password"},template=None,native=True)
        console={"Cookie":headers.get("Set-Cookie").split(";",1)[0],"X-Requested-With":"XMLHttpRequest","Origin":self.fixture.origin}
        _,_,created=self.fixture.wire("POST","/api/management-keys",{"name":"g6-queued-writer","scopes":["accounts:write","accounts:read","tasks:read"]},headers=console)
        key=created["data"]
        writer=self.client_module.AitoapiManagementClient(self.fixture.origin,key["token"],allow_loopback=True)
        self.addCleanup(writer.close)
        queued=writer.submit_import([{"clientRef":"g6-revoked","credentials":synthetic_credentials("revoked")}],"g6-ok","g6-revoke-queued")
        self.assertEqual(self.client.task(queued["taskId"])["status"],"queued")
        status,_,_=self.fixture.wire("DELETE","/api/management-keys/"+key["key"]["id"],{},headers=console)
        self.assertEqual(status,200)
        cancelled=self.fixture.poll(queued["taskId"])
        self.assertEqual(cancelled["status"],"cancelled")
        self.assertFalse(cancelled["items"][0].get("upload"))
        self.assertEqual(self.client.accounts_page()["total"],1)
        self.client._request("POST",f"/api/manage/v1/tasks/{running['taskId']}/cancel",body={})
        self.fixture.poll(running["taskId"])
        self.assertEqual(self.fixture.metrics()["generated"],1)
        self.assertEqual(self.fixture.metrics()["mutations"]["import"],1)

    def test_cancel_before_and_after_durable_commit_keeps_original_evidence(self):
        committed=0
        for point in ("before-prepare","before-finalize"):
            with self.subTest(point=point):
                self.fixture.barrier(point)
                ack=self.client.submit_import([{"clientRef":"g6-cancel-"+point,"credentials":synthetic_credentials("cancel-"+point)}],"g6-ok","g6-cancel-"+point)
                self.fixture.wait_barrier(point)
                self.client._request("POST",f"/api/manage/v1/tasks/{ack['taskId']}/cancel",body={})
                self.fixture.release()
                task=self.fixture.poll(ack["taskId"])
                self.assertIn(task["status"],{"cancelled","failed"})
                if point=="before-finalize":
                    committed+=1
                    receipt=task["items"][0]["upload"]
                    self.assertEqual(receipt["status"],"committed")
                    self.assertEqual(self.client.account(receipt["accountId"])["credentialVersion"],receipt["credentialVersion"])
                else:
                    self.assertFalse(task["items"][0].get("upload"))
                self.assertEqual(self.client.accounts_page()["total"],committed)
                self.assertEqual(self.fixture.metrics()["generated"],0)

    def test_auth_expired_recovery_keeps_identity_and_has_no_implicit_model(self):
        _,ack,task,account=self.imported("recover")
        receipt=task["items"][0]["upload"]
        expired=self.fixture.auth_expired(account["accountId"])
        self.assertIs(expired["expired"],True)
        self.assertEqual((expired["disabledReason"],expired["disabledStatus"]),("auth_required",401))
        self.assertEqual(expired["credentialVersion"],account["credentialVersion"])
        self.assertGreater(expired["stateVersion"],account["stateVersion"])
        fresh=self.client.account(account["accountId"])
        self.assertEqual(fresh,expired)
        replacement=self.client.submit_replace(fresh["accountId"],synthetic_credentials("recover",2),"g6-recover",
            credential_version=fresh["credentialVersion"],state_version=fresh["stateVersion"])
        self.assertEqual(self.fixture.poll(replacement["taskId"])["status"],"succeeded")
        recovered=self.client.account(fresh["accountId"])
        self.assertEqual((recovered["accountId"],recovered["index"]),(account["accountId"],account["index"]))
        self.assertIs(recovered["expired"],False)
        self.assertIs(recovered["enabled"],True)
        self.assertIsNone(recovered["disabledReason"])
        self.assertEqual(self.client.task(ack["taskId"])["items"][0]["upload"],receipt)
        self.assertEqual(self.fixture.metrics()["generated"],0)
        manual=self.client.disable_if_version(recovered["accountId"],recovered["credentialVersion"],recovered["stateVersion"])
        status,_,_=self.fixture.wire("POST","/__g6__/auth-expired",{"accountId":manual["accountId"]},native=True)
        self.assertEqual(status,409)
        stale=self.client.submit_replace(recovered["accountId"],synthetic_credentials("recover",3),"g6-recover-stale",
            credential_version=recovered["credentialVersion"],state_version=recovered["stateVersion"])
        task=self.fixture.poll(stale["taskId"])
        self.assertEqual(task["items"][0]["error"]["code"],"VERSION_CONFLICT")
        self.assertEqual(self.client.account(manual["accountId"]),manual)
        self.assertEqual(self.fixture.metrics()["generated"],0)


    def block_worker(self, account_id, idem="g6-snapshot-blocker"):
        before=self.fixture.metrics()["generated"]
        accepted=self.client.submit_test(account_id,"g6-cancel",idem)
        deadline=time.monotonic()+10
        while self.fixture.metrics()["generated"]<before+1 and time.monotonic()<deadline:
            time.sleep(.02)
        self.assertEqual(self.fixture.metrics()["generated"],before+1)
        self.assertEqual(self.client.task(accepted["taskId"])["status"],"running")
        return accepted

    def unblock_worker(self, accepted):
        self.client._request("POST",f"/api/manage/v1/tasks/{accepted['taskId']}/cancel",body={})
        self.assertIn(self.fixture.poll(accepted["taskId"])["status"],{"cancelled","failed"})

    def queued_request(self, method, path, body, idem):
        accepted=self.client._request(method,path,body=body,idempotency_key=idem)
        self.assertEqual(accepted["status"],"queued")
        self.assertEqual(self.client.task(accepted["taskId"])["status"],"queued")
        return accepted

    def assert_snapshot_conflict(self, accepted, unchanged, *, target_calls=1):
        task=self.fixture.poll(accepted["taskId"])
        self.assertEqual(task["status"],"failed")
        errors=[task.get("error",{})]+[item.get("error",{}) for item in task["items"]]
        self.assertIn("VERSION_CONFLICT",[error.get("code") for error in errors])
        self.assertFalse(any(item.get("result",{}).get("stage")=="model_verified" for item in task["items"]))
        self.assertEqual(self.client.account(unchanged["accountId"]),unchanged)
        metrics=self.fixture.metrics()
        self.assertEqual(metrics["generated"],target_calls)
        self.assertEqual(metrics["opened"],target_calls)
        return task

    def queued_after_manual_disable(self, kind):
        _,_,_,account=self.imported("snapshot-"+kind)
        blocker=self.block_worker(account["accountId"])
        if kind=="batch-enable":
            method,path,body="POST","/api/manage/v1/accounts/batch",{"action":"enable","accountIds":[account["accountId"]]}
        elif kind=="reload-auth":
            method,path,body="POST","/api/manage/v1/system/reload-auth",{}
        else:
            method,path,body="POST",f"/api/manage/v1/accounts/{account['accountId']}/{kind}",{}
        idem="g6-queued-"+kind
        accepted=self.queued_request(method,path,body,idem)
        manual=self.client.disable_if_version(account["accountId"],account["credentialVersion"],account["stateVersion"])
        self.assertIs(manual["enabled"],False)
        # Same submission cannot acquire a newer snapshot through replay.
        self.assertEqual(self.client._request(method,path,body=body,idempotency_key=idem),accepted)
        self.unblock_worker(blocker)
        self.assert_snapshot_conflict(accepted,manual)
        self.assertEqual(self.client._request(method,path,body=body,idempotency_key=idem),accepted)
        self.assertEqual(self.client.account(manual["accountId"]),manual)

    def test_queued_batch_enable_cannot_undo_newer_manual_disable(self):
        self.queued_after_manual_disable("batch-enable")

    def test_queued_archive_rejects_newer_manual_state(self):
        self.queued_after_manual_disable("archive")

    def test_queued_reload_rejects_newer_manual_state(self):
        self.queued_after_manual_disable("reload")

    def test_queued_reload_auth_rejects_newer_manual_state(self):
        self.queued_after_manual_disable("reload-auth")

    def test_queued_restore_rejects_native_restore_with_new_version(self):
        _,_,_,account=self.imported("snapshot-restore")
        _,_,_,block_account=self.imported("snapshot-restore-blocker")
        archived=self.client._request("POST",f"/api/manage/v1/accounts/{account['accountId']}/archive",body={},idempotency_key="g6-snapshot-archive-setup")
        self.assertEqual(self.fixture.poll(archived["taskId"])["status"],"succeeded")
        before=self.client.account(account["accountId"])
        self.assertIs(before["archived"],True)
        blocker=self.block_worker(block_account["accountId"])
        path=f"/api/manage/v1/accounts/{account['accountId']}/restore"
        accepted=self.queued_request("POST",path,{},"g6-snapshot-restore")
        restored=self.fixture.native_restore(account["accountId"])
        self.assertIs(restored["archived"],False)
        self.assertIs(restored["enabled"],False)
        self.assertGreater(restored["stateVersion"],before["stateVersion"])
        self.unblock_worker(blocker)
        self.assert_snapshot_conflict(accepted,restored)

    def test_queued_model_test_rejects_newer_state_before_target_open(self):
        _,_,_,account=self.imported("snapshot-model-state")
        blocker=self.block_worker(account["accountId"])
        accepted=self.queued_request("POST",f"/api/manage/v1/accounts/{account['accountId']}/test",{"mode":"model","model":"g6-ok"},"g6-snapshot-model-state")
        manual=self.client.disable_if_version(account["accountId"],account["credentialVersion"],account["stateVersion"])
        self.unblock_worker(blocker)
        self.assert_snapshot_conflict(accepted,manual)

    def test_queued_connection_test_rejects_newer_state_before_target_open(self):
        _,_,_,account=self.imported("snapshot-connection-state")
        blocker=self.block_worker(account["accountId"])
        accepted=self.queued_request("POST",f"/api/manage/v1/accounts/{account['accountId']}/test",{"mode":"connection","model":"g6-ok"},"g6-snapshot-connection-state")
        manual=self.client.disable_if_version(account["accountId"],account["credentialVersion"],account["stateVersion"])
        self.unblock_worker(blocker)
        self.assert_snapshot_conflict(accepted,manual)

    def test_queued_model_test_rejects_earlier_queued_credential_replace(self):
        _,_,_,account=self.imported("snapshot-model-credential")
        blocker=self.block_worker(account["accountId"])
        replacement=self.client.submit_replace(account["accountId"],synthetic_credentials("snapshot-model-credential",2),"g6-snapshot-prior-replace",
            credential_version=account["credentialVersion"],state_version=account["stateVersion"])
        self.assertEqual(self.client.task(replacement["taskId"])["status"],"queued")
        accepted=self.queued_request("POST",f"/api/manage/v1/accounts/{account['accountId']}/test",{"mode":"model","model":"g6-ok"},"g6-snapshot-model-credential")
        self.unblock_worker(blocker)
        self.assertEqual(self.fixture.poll(replacement["taskId"])["status"],"succeeded")
        replaced=self.client.account(account["accountId"])
        self.assertGreater(replaced["credentialVersion"],account["credentialVersion"])
        self.assert_snapshot_conflict(accepted,replaced)

    def test_queued_legacy_replace_rejects_newer_state_before_verification(self):
        _,_,_,account=self.imported("snapshot-legacy")
        blocker=self.block_worker(account["accountId"])
        accepted=self.queued_request("PUT",f"/api/manage/v1/accounts/{account['accountId']}/credentials",synthetic_credentials("snapshot-legacy",2),"g6-snapshot-legacy")
        manual=self.client.disable_if_version(account["accountId"],account["credentialVersion"],account["stateVersion"])
        self.unblock_worker(blocker)
        self.assert_snapshot_conflict(accepted,manual)

    def test_queued_snapshot_survives_restart_and_same_key_idempotent_replay(self):
        _,_,_,account=self.imported("snapshot-restart")
        self.block_worker(account["accountId"])
        path="/api/manage/v1/accounts/batch"
        body={"action":"enable","accountIds":[account["accountId"]]}
        idem="g6-snapshot-restart"
        accepted=self.queued_request("POST",path,body,idem)
        manual=self.client.disable_if_version(account["accountId"],account["credentialVersion"],account["stateVersion"])
        same_key=self.fixture.token()
        self.fixture.restart(crash=True)
        # Idempotency is namespaced by the original persisted Key, not the
        # freshly-created synthetic admin Key in the new ready record.
        self.reconnect(token=same_key)
        self.assert_snapshot_conflict(accepted,manual,target_calls=0)
        self.assertEqual(self.client._request("POST",path,body=body,idempotency_key=idem),accepted)
        self.assertEqual(self.fixture.metrics()["admittedTasks"],0)
        self.assertEqual(self.fixture.metrics()["generated"],0)

    def test_claim_failure_before_sql_commit_does_not_strand_queued_task(self):
        _,_,_,account=self.imported("claim-transient")
        before=self.fixture.metrics()["claimAttempts"]
        self.fixture.claim_fault("before",2)
        accepted=self.client.submit_test(account["accountId"],"g6-ok","g6-claim-transient")
        # No second Submit or startup RecoverTasks is used to wake this task.
        task=self.fixture.poll(accepted["taskId"])
        self.assertEqual(task["status"],"succeeded")
        metrics=self.fixture.metrics()
        self.assertGreaterEqual(metrics["claimAttempts"],before+3)
        self.assertEqual(metrics["claimFaults"],2)
        self.assertEqual(metrics["generated"],1)
        self.assertEqual(metrics["opened"],1)

    def test_claim_result_lost_after_sql_commit_does_not_repeat_model(self):
        _,_,_,account=self.imported("claim-ambiguous")
        self.fixture.claim_fault("after")
        accepted=self.client.submit_test(account["accountId"],"g6-ok","g6-claim-ambiguous")
        task=self.fixture.poll(accepted["taskId"])
        self.assertEqual(task["status"],"interrupted")
        self.assertEqual(self.fixture.metrics()["claimFaults"],1)
        self.assertEqual(self.fixture.metrics()["generated"],0)
        self.assertEqual(self.fixture.metrics()["opened"],0)
        self.fixture.restart(crash=True)
        self.reconnect()
        self.assertEqual(self.client.task(accepted["taskId"])["status"],"interrupted")
        self.assertEqual(self.fixture.metrics()["generated"],0)


def source_check(manager_root):
    spec = json.loads((PROJECT/"docs/management/openapi.json").read_text(encoding="utf-8"))
    refs = []

    def visit(value):
        if isinstance(value,dict):
            if "$ref" in value:
                ref=value["$ref"]
                if not ref.startswith("#/"):
                    raise AssertionError("external schema reference")
                current=spec
                for part in ref[2:].split("/"):
                    current=current[part.replace("~1","/").replace("~0","~")]
                refs.append(ref)
            for item in value.values(): visit(item)
        elif isinstance(value,list):
            for item in value: visit(item)
    visit(spec)
    tree=ast.parse((manager_root/"enterprise/aitoapi_client.py").read_text(encoding="utf-8-sig"))
    errors=next(ast.literal_eval(node.value) for node in tree.body if isinstance(node,ast.Assign) and any(isinstance(target,ast.Name) and target.id=="REMOTE_ERRORS" for target in node.targets))
    schemas=spec["components"]["schemas"]
    # The examples are assertions against the frozen spec, not HTTP evidence.
    extension=schemas["PlaygroundVerificationResult"]
    assert extension.get("additionalProperties") is False
    assert "actualChannel" in extension["required"]
    assert "actualChannel" not in schemas["VerificationResult"]["properties"]
    assert len(errors)==21
    for code,(status,message) in errors.items():
        assert code in schemas["ErrorCode"]["enum"]
        assert code in json.dumps(spec) and message in json.dumps(spec)
        # Keep status parity checked against actual Go error catalog literals.
        go=(PROJECT/"internal/managementcontract/errors.go").read_text(encoding="utf-8")
        assert f'{{{status}, "{message}"}}' in go
    print(json.dumps({"kind":"source-check-only","openapiRefsResolved":len(refs),"legacyErrorPairs":len(errors),"httpExecuted":False}))


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manager-root",type=Path,required=True)
    parser.add_argument("--fixture-exe",type=Path)
    parser.add_argument("--check",action="store_true")
    parser.add_argument("--test",action="append",help="Run only this RealHandlerAcceptance method (repeatable)")
    parser.add_argument("--list-tests",action="store_true",help="List prepared test methods without starting a fixture")
    args=parser.parse_args()
    if args.list_tests:
        print("\n".join(unittest.defaultTestLoader.getTestCaseNames(RealHandlerAcceptance)))
        return 0
    if args.check:
        source_check(args.manager_root.resolve())
        return 0
    if not args.fixture_exe:
        parser.error("--fixture-exe is required for real-handler acceptance")
    # Fail once on an OS execution block, before unittest starts individual
    # fixtures. Do not repeat consumed starts or seek another execution context.
    probe=FixtureProcess(args.fixture_exe)
    try:
        probe.start()
    except OSError as error:
        print(json.dumps({"kind":"real-handler-acceptance","state":"BLOCKED",
                          "errorType":type(error).__name__,"winerror":getattr(error,"winerror",None),
                          "message":str(error),"testsRun":0,"passed":False}))
        return 2
    finally:
        probe.close()
    RealHandlerAcceptance.executable=args.fixture_exe
    RealHandlerAcceptance.manager_root=args.manager_root.resolve()
    suite=(unittest.TestSuite(RealHandlerAcceptance(name) for name in args.test) if args.test
           else unittest.defaultTestLoader.loadTestsFromTestCase(RealHandlerAcceptance))
    result=unittest.TextTestRunner(verbosity=2).run(suite)
    print(json.dumps({"kind":"real-handler-acceptance","testsRun":result.testsRun,
                      "failures":len(result.failures),"errors":len(result.errors),"skipped":len(result.skipped),
                      "passed":result.wasSuccessful() and result.testsRun>0 and not result.skipped}))
    return 0 if result.wasSuccessful() and result.testsRun>0 and not result.skipped else 1


if __name__=="__main__":
    raise SystemExit(main())

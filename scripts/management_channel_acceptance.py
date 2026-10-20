"""G6 V01-V12: real Go HTTP and actual Manager selectable-channel clients.

Only the Go fixture's upstream is synthetic. No HTTP result, Manager policy,
evidence validator or readback is replaced. Go source/fixture is not edited here.
"""
from __future__ import annotations

import argparse
from copy import deepcopy
from datetime import datetime, timezone
import hashlib
import importlib
import json
from pathlib import Path
import re
import sys
import time
import unittest
from urllib.parse import urlsplit

import management_acceptance as base

sys.dont_write_bytecode = True
EXPECTED_FIXTURE_SHA256 = "BAF49E36D8977B63C88576037E97195E329B8F631FF9C02F146425DEC677B514"
BOTH = ["build", "playground"]
REQUEST_ID = re.compile(r"req_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}")
MANAGER_SOURCES = (
    "enterprise/aistudio2api_client.py", "enterprise/aistudio2api_readback.py",
    "enterprise/aistudio2api_verification.py", "enterprise/aistudio2api_evidence.py",
    "enterprise/aistudio2api_contract.py", "enterprise/aistudio2api_remote_status.py",
    "enterprise/aitoapi_client.py", "enterprise/aitoapi_readback.py",
    "enterprise/aitoapi_evidence.py", "enterprise/aitoapi_outcomes.py",
    "enterprise/aitoapi_repository.py", "enterprise/aitoapi_crypto.py",
    "enterprise/aitoapi_storage_state.py", "core/http_clients.py",
)


def sha256(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest().upper()


def manager_hashes(root):
    return {name: sha256(root / name) for name in MANAGER_SOURCES}


class ChannelSession(base.RecordingSession):
    """Record only the safe channel-selection field; all requests are real."""
    def __init__(self, requests_module):
        super().__init__(requests_module)
        self.selections = []

    def request(self, method, url, **kwargs):
        if method == "POST" and urlsplit(url).path.endswith("/test"):
            body = kwargs.get("json") or {}
            self.selections.append(deepcopy(body.get("channels")))
        return super().request(method, url, **kwargs)


class ChannelAcceptance(unittest.TestCase):
    executable = None
    manager_root = None

    @classmethod
    def setUpClass(cls):
        sys.path.insert(0, str(cls.manager_root))
        cls.new_module = importlib.import_module("enterprise.aistudio2api_client")
        cls.old_module = importlib.import_module("enterprise.aitoapi_client")
        cls.readback_module = importlib.import_module("enterprise.aistudio2api_readback")
        cls.evidence_module = importlib.import_module("enterprise.aistudio2api_evidence")
        cls.requests_module = importlib.import_module("requests")

    def setUp(self):
        self.fixture = base.FixtureProcess(self.executable)
        self.addCleanup(self.fixture.close)
        self.fixture.start()
        self.connect()

    def connect(self, channels=None):
        self.session = ChannelSession(self.requests_module)
        self.client = self.new_module.AIStudio2APIManagementClient(
            self.fixture.origin, self.fixture.token(), session=self.session,
            allow_loopback=True, validation_channels=channels,
        )
        self.legacy = self.old_module.AitoapiManagementClient(
            self.fixture.origin, self.fixture.token(), allow_loopback=True,
        )
        self.addCleanup(self.client.close)
        self.addCleanup(self.legacy.close)

    def imported(self, label="channel"):
        ack = self.client.submit_import(
            [{"clientRef": "g6-"+label, "credentials": base.synthetic_credentials(label)}],
            "g6-ok", "g6-import-"+label,
        )
        task = self.fixture.poll(ack["taskId"])
        self.assertEqual(task["status"], "succeeded")
        self.assertEqual(task["items"][0]["upload"]["status"], "committed")
        self.assert_calls([], model="g6-ok")
        return self.client.account(task["items"][0]["accountId"])

    def assert_calls(self, channels, *, account=None, model="g6-ok"):
        stats = self.fixture.metrics()
        self.assertEqual(stats["generated"], len(channels))
        self.assertEqual(stats["generationCalls"], len(channels))
        self.assertEqual(stats["buildCalls"], channels.count("build"))
        self.assertEqual(stats["playgroundCalls"], channels.count("playground"))
        self.assertEqual([event["channel"] for event in stats["events"]], channels)
        self.assertTrue(all(event["mode"] == "model" for event in stats["events"]))
        if account is not None:
            self.assertTrue(all(event["accountId"] == account["accountId"] for event in stats["events"]))
        if model is not None:
            self.assertTrue(all(event["model"] == model for event in stats["events"]))
        return stats

    def checkpoint(self, task, account, model, channels):
        return {
            "operation": "test", "target_model": model, "validation_channels": list(channels),
            "server_origin": self.fixture.origin, "remote_task_id": task["taskId"],
            "remote_management_key_id": task["createdByKeyId"], "remote_account_id": account["accountId"],
            "source_version": 1, "submitted_at": task["createdAt"],
        }

    def manager_proof(self, task, account, model, channels, verified):
        item = task["items"][0]
        checkpoint = self.checkpoint(task, account, model, channels)
        readback = self.readback_module.AIStudio2APITaskReadback()
        self.assertEqual(readback.verified_result(task, item, account, model, channels), verified)
        finding = readback.inspect(
            {"checkpoint": checkpoint, "remote_account_id": account["accountId"]},
            server={"origin": self.fixture.origin}, email=account["accountName"], client=self.client,
        )
        self.assertEqual(finding["finding"], "model_verified" if verified else "target_model_unverified")
        evidence = self.evidence_module.task_outcome(
            {"server_id": 1, "remote_account_id": account["accountId"]},
            checkpoint, task, item, account,
        )
        clean = self.evidence_module.validate_evidence(evidence, instance_id=1, remote_account_id=account["accountId"])
        proof = clean["verification"]
        self.assertEqual(proof["channels"], channels)
        self.assertEqual(proof["status"] == "succeeded", verified)
        self.assertEqual([child["requested_channel"] for child in proof["channel_results"]], channels)
        if len(channels) == 2:
            self.assertIsNone(proof["actual_channel"])
        return proof

    def assert_success(self, task, account, channels, *, model="g6-ok"):
        self.assertEqual(task["status"], "succeeded")
        item = task["items"][0]
        self.assertEqual(item["status"], "succeeded")
        self.assertEqual((item["accountId"], item["index"]), (account["accountId"], account["index"]))
        result = item["result"]
        self.assertIs(result["success"], True)
        self.assertEqual((result["stage"], result["channels"]), ("model_verified", channels))
        self.assertEqual(len(result["channelResults"]), len(channels))
        requests = []
        for requested, child in zip(channels, result["channelResults"]):
            self.assertEqual((child["requestedChannel"], child["actualChannel"]), (requested, requested))
            self.assertIs(child["success"], True)
            self.assertEqual((child["stage"], child["authIndex"], child["model"]), ("model_verified", account["index"], model))
            self.assertEqual((child["credentialVersion"], child["stateVersion"]), (account["credentialVersion"], account["stateVersion"]))
            self.assertTrue(REQUEST_ID.fullmatch(child["requestId"]))
            self.assertIs(type(child["upstreamStatus"]), int)
            self.assertTrue(200 <= child["upstreamStatus"] < 300)
            self.assertNotIn("error", child)
            requests.append(child["requestId"])
        self.assertEqual(len(set(requests)), len(channels))
        if len(channels) == 1:
            child = result["channelResults"][0]
            self.assertEqual(result["actualChannel"], channels[0])
            self.assertEqual(result["requestId"], child["requestId"])
            self.assertEqual(result["upstreamStatus"], child["upstreamStatus"])
        else:
            self.assertIn(result.get("actualChannel"), (None, ""))
            self.assertIn(result.get("requestId"), (None, ""))
            self.assertIsNone(result["upstreamStatus"])
        return self.manager_proof(task, account, model, channels, True)

    def wait_completed_build(self, task_id):
        deadline = time.monotonic()+10
        while time.monotonic() < deadline:
            task = self.client.task(task_id)
            result = task["items"][0].get("result") or {}
            children = result.get("channelResults") or []
            if (task["status"] == "running" and children and children[0].get("success") is True
                    and self.fixture.metrics()["playgroundCalls"] == 1):
                self.assertEqual(children[0]["requestedChannel"], "build")
                return task, deepcopy(children[0])
            time.sleep(.02)
        self.fail("completed Build child was not durably readable while Playground was pending")

    def test_v01_capabilities_preflight_default_build_without_model(self):
        capabilities = self.client.capabilities()
        self.assertEqual(capabilities["verificationChannels"], BOTH)
        self.assertEqual(capabilities["defaultVerificationChannels"], ["build"])
        self.assertIs(capabilities["multiChannelVerification"], True)
        preflight = self.client.preflight()
        self.assertTrue(preflight["connection_valid"])
        self.assertIs(preflight["readiness"]["ready"], False)
        self.assertEqual(self.client.tasks_page()["total"], 0)
        self.assert_calls([])

    def test_v02_omitted_wire_channels_execute_only_build(self):
        account = self.imported()
        ack = self.legacy._request("POST", f"/api/manage/v1/accounts/{account['accountId']}/test",
            body={"mode":"model", "model":"g6-ok"}, idempotency_key="g6-wire-default")
        self.assert_success(self.fixture.poll(ack["taskId"]), account, ["build"])
        self.assert_calls(["build"], account=account)

    def test_v03_actual_new_manager_client_defaults_to_build(self):
        account = self.imported()
        ack = self.client.submit_test(account["accountId"], "g6-ok", "g6-manager-default")
        task = self.fixture.poll(ack["taskId"])
        self.assertEqual(self.session.selections, [["build"]])
        self.assert_success(task, account, ["build"])
        self.assert_calls(["build"], account=account)
        # Old four-argument evidence is historical Playground, never Build proof.
        self.assertFalse(self.readback_module.AIStudio2APITaskReadback.verified_result(task, task["items"][0], account, "g6-ok"))

    def test_v04_explicit_playground_new_and_legacy_clients(self):
        account = self.imported()
        ack = self.client.submit_test(account["accountId"], "g6-ok", "g6-manager-playground", channels=["playground"])
        self.assert_success(self.fixture.poll(ack["taskId"]), account, ["playground"])
        ack = base.submit_playground_test(self.legacy, account["accountId"], "g6-ok", "g6-legacy-playground")
        task = self.fixture.poll(ack["taskId"])
        self.assert_success(task, account, ["playground"])
        self.assertTrue(self.readback_module.AIStudio2APITaskReadback.verified_result(task, task["items"][0], account, "g6-ok"))
        self.assert_calls(["playground", "playground"], account=account)

    def test_v05_dual_success_uses_two_children_and_canonical_selection(self):
        account = self.imported()
        ack = self.client.submit_test(account["accountId"], "g6-ok", "g6-both", channels=["playground", "build"])
        task = self.fixture.poll(ack["taskId"])
        self.assertEqual(self.session.selections, [BOTH])
        proof = self.assert_success(task, account, BOTH)
        self.assertTrue(all(child["status"] == "succeeded" for child in proof["channel_results"]))
        self.assert_calls(BOTH, account=account)
        self.assertFalse(self.readback_module.AIStudio2APITaskReadback.verified_result(task, task["items"][0], account, "g6-ok", ["build"]))

    def test_v06_playground_failure_preserves_build_but_is_not_dual_success(self):
        account = self.imported()
        model = "g6-playground-fail"
        ack = self.client.submit_test(account["accountId"], model, "g6-both-failed", channels=BOTH)
        task = self.fixture.poll(ack["taskId"])
        self.assertNotEqual(task["status"], "succeeded")
        result = task["items"][0]["result"]
        self.assertIs(result["success"], False)
        self.assertEqual(result["channels"], BOTH)
        self.assertEqual([child["success"] for child in result["channelResults"]], [True, False])
        self.assertEqual(result["channelResults"][1]["error"]["code"], "VERIFICATION_FAILED")
        proof = self.manager_proof(task, account, model, BOTH, False)
        self.assertEqual([child["status"] for child in proof["channel_results"]], ["succeeded", "failed"])
        self.assert_calls(BOTH, account=account, model=model)

    def test_v07_wrong_account_model_or_channel_never_falls_back(self):
        account = self.imported()
        channels_seen = []
        for model in ("g6-wrongacct", "g6-wrongmodel", "g6-wrongchannel"):
            for channel in BOTH:
                with self.subTest(model=model, channel=channel):
                    ack = self.client.submit_test(account["accountId"], model, "g6-"+model+"-"+channel, channels=[channel])
                    task = self.fixture.poll(ack["taskId"])
                    self.assertNotEqual(task["status"], "succeeded")
                    result = task["items"][0]["result"]
                    self.assertIs(result["success"], False)
                    self.assertEqual(len(result["channelResults"]), 1)
                    child = result["channelResults"][0]
                    self.assertEqual(child["requestedChannel"], channel)
                    self.assertIs(child["success"], False)
                    self.assertEqual(child["error"]["code"], "VERIFICATION_FAILED")
                    self.manager_proof(task, account, model, [channel], False)
                    channels_seen.append(channel)
                    self.assert_calls(channels_seen, account=account, model=None)

    def test_v08_cancel_preserves_completed_build_without_dual_success(self):
        account = self.imported()
        model = "g6-cancel-after-build"
        ack = self.client.submit_test(account["accountId"], model, "g6-both-cancel", channels=BOTH)
        pending, build_child = self.wait_completed_build(ack["taskId"])
        interim = self.evidence_module.task_outcome({"server_id":1, "remote_account_id":account["accountId"]},
            self.checkpoint(pending, account, model, BOTH), pending, pending["items"][0], account)
        if interim["verification"]["channel_results"][0]["status"] != "succeeded":
            diagnostic={"phase":"build-complete-playground-pending", "taskStatus":pending["status"],
                        "itemIndex":pending["items"][0].get("index"), "accountIndex":account["index"],
                        "childAuthIndex":build_child["authIndex"], "childSuccess":build_child["success"],
                        "childStage":build_child["stage"], "childRequestedChannel":build_child["requestedChannel"],
                        "childActualChannel":build_child["actualChannel"],
                        "managerChildStatus":interim["verification"]["channel_results"][0]["status"]}
            path=base.PROJECT/"runtime/management-development/g6/channel-v08-diagnostic.json"
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(json.dumps(diagnostic, indent=2)+"\n", encoding="utf-8")
        # Keep this failure visible while still exercising the actual HTTP
        # cancellation below; no policy/result is patched to get past it.
        with self.subTest(phase="completed-child-readable-by-manager-before-cancel"):
            self.assertEqual(interim["verification"]["channel_results"][0]["status"], "succeeded")
        self.assertNotEqual(interim["verification"]["status"], "succeeded")
        self.client._request("POST", f"/api/manage/v1/tasks/{ack['taskId']}/cancel", body={})
        task = self.fixture.poll(ack["taskId"])
        self.assertEqual(task["status"], "cancelled")
        result = task["items"][0]["result"]
        self.assertEqual(result["channelResults"][0], build_child)
        self.assertIs(result["success"], False)
        self.assertFalse(any(child["success"] for child in result["channelResults"][1:]))
        proof = self.manager_proof(task, account, model, BOTH, False)
        self.assertEqual(proof["channel_results"][0]["status"], "succeeded")
        self.assert_calls(BOTH, account=account, model=model)

    def test_v09_verify_false_with_both_upload_replace_and_readback_has_zero_calls(self):
        credentials = base.synthetic_credentials("no-verify")
        ack = self.client._request("POST", "/api/manage/v1/accounts/import",
            body={"items":[{"clientRef":"g6-no-verify", "credentials":credentials}], "verify":False, "channels":BOTH},
            idempotency_key="g6-no-verify")
        original = self.fixture.poll(ack["taskId"])
        self.assertEqual(original["status"], "succeeded")
        receipt = deepcopy(original["items"][0]["upload"])
        account = self.client.account(receipt["accountId"])
        replacement = self.client._request("PUT", f"/api/manage/v1/accounts/{account['accountId']}/credentials",
            body={"credentials":base.synthetic_credentials("no-verify", 2), "verify":False, "channels":BOTH,
                  "expectedCredentialVersion":account["credentialVersion"], "expectedStateVersion":account["stateVersion"]},
            idempotency_key="g6-no-verify-replace")
        replaced = self.fixture.poll(replacement["taskId"])
        self.assertEqual(replaced["status"], "succeeded")
        current = self.client.account(account["accountId"])
        self.assertGreater(current["credentialVersion"], account["credentialVersion"])
        self.assertEqual(self.client.task(ack["taskId"])["items"][0]["upload"], receipt)
        self.assertEqual(replaced["items"][0]["result"]["stage"], "uploaded")
        self.assert_calls([])

    def test_v10_lost_model_ack_stays_unlocated_with_read_only_readback(self):
        account = self.imported()
        submitted = datetime.now(timezone.utc)
        self.session.drop_next_write = True
        with self.assertRaises(self.old_module.AitoapiClientError) as caught:
            self.client.submit_test(account["accountId"], "g6-ok", "g6-lost-both", channels=BOTH)
        self.assertEqual(caught.exception.code, "NETWORK_RESULT_UNKNOWN")
        boundary = len(self.session.calls)
        # TestRequest has no clientRef. Account/key/time candidates cannot prove
        # which submission was ours; never inject a fixture-discovered taskId.
        checkpoint={"operation":"test", "target_model":"g6-ok", "validation_channels":BOTH,
                    "server_origin":self.fixture.origin, "submitted_at":submitted.isoformat(),
                    "remote_account_id":account["accountId"],
                    "remote_management_key_id":self.fixture.ready["keys"]["admin"]["keyId"]}
        readback=self.readback_module.AIStudio2APITaskReadback()
        finding=readback.inspect({"checkpoint":checkpoint,"remote_account_id":account["accountId"]},
            server={"origin":self.fixture.origin},email=account["accountName"],client=self.client)
        self.assertIn(finding["finding"],{"account_candidate_only","remote_result_unknown"})
        self.assertFalse(finding.get("remote_task_id"))
        self.assertFalse(finding.get("remote_task"))
        self.assertIsNot(finding.get("available"),True)
        self.assertNotIn("verification",finding)
        self.assertNotIn("last_verified",finding)
        self.assertTrue(self.session.calls[boundary:])
        self.assertTrue(all(method == "GET" for method, _ in self.session.calls[boundary:]))
        self.assertEqual(sum(method == "POST" and path.endswith("/test") for method, path in self.session.calls), 1)
        # Test-only counters prove the accepted fake upstream work was not
        # replayed. They are not remote task identity or Manager success proof.
        deadline=time.monotonic()+10
        while self.fixture.metrics()["closed"]<2 and time.monotonic()<deadline:
            time.sleep(.02)
        self.assertEqual(self.fixture.metrics()["closed"],2)
        self.assert_calls(BOTH, account=account)

    def test_v11_crash_retains_completed_build_and_does_not_replay_playground(self):
        account = self.imported()
        model = "g6-cancel-after-build"
        ack = self.client.submit_test(account["accountId"], model, "g6-both-crash", channels=BOTH)
        _, build_child = self.wait_completed_build(ack["taskId"])
        self.assert_calls(BOTH, account=account, model=model)
        self.fixture.restart(crash=True)
        self.client.close()
        self.legacy.close()
        self.connect(BOTH)
        boundary = len(self.session.calls)
        task = self.client.task(ack["taskId"])
        self.assertEqual(task["status"], "interrupted")
        self.assertEqual(task["items"][0]["result"]["channelResults"][0], build_child)
        self.assertIs(task["items"][0]["result"]["success"], False)
        proof=self.manager_proof(task, account, model, BOTH, False)
        self.assertEqual(proof["channel_results"][0]["status"], "succeeded")
        self.assertTrue(all(method == "GET" for method, _ in self.session.calls[boundary:]))
        self.assert_calls([])

    def test_v12_invalid_selection_is_proven_not_admitted(self):
        account = self.imported()
        before = self.client.tasks_page()["total"]
        for i, channels in enumerate(([], ["build","build"], ["both"], ["unknown"], None,
                                      "build", True, ["build","playground","build"])):
            with self.subTest(channels=channels):
                with self.assertRaises(self.old_module.AitoapiClientError) as caught:
                    self.legacy._request("POST", f"/api/manage/v1/accounts/{account['accountId']}/test",
                        body={"mode":"model", "model":"g6-ok", "channels":channels}, idempotency_key="g6-invalid-"+str(i))
                self.assertEqual((caught.exception.code, caught.exception.status), ("INVALID_REQUEST",400))
                self.assertTrue(caught.exception.valid_error_envelope)
                self.assertTrue(caught.exception.not_admitted)
        self.assertEqual(self.client.tasks_page()["total"], before)
        self.assert_calls([])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manager-root", type=Path, required=True)
    parser.add_argument("--fixture-exe", type=Path)
    parser.add_argument("--fixture-sha256", default=EXPECTED_FIXTURE_SHA256)
    parser.add_argument("--list-tests", action="store_true")
    parser.add_argument("--test", action="append")
    parser.add_argument("--output", type=Path, default=base.PROJECT/"runtime/management-development/g6/channel-result.json")
    args = parser.parse_args()
    if args.list_tests:
        print("\n".join(unittest.defaultTestLoader.getTestCaseNames(ChannelAcceptance)))
        return 0
    if not args.fixture_exe:
        parser.error("--fixture-exe is required for real HTTP acceptance")
    fixture_before = sha256(args.fixture_exe)
    if fixture_before != args.fixture_sha256.upper():
        raise SystemExit("Fixture SHA256 differs from the dispatcher handoff; no tests started")
    before = manager_hashes(args.manager_root)
    ChannelAcceptance.executable = args.fixture_exe
    ChannelAcceptance.manager_root = args.manager_root.resolve()
    suite = (unittest.TestSuite(ChannelAcceptance(name) for name in args.test) if args.test
             else unittest.defaultTestLoader.loadTestsFromTestCase(ChannelAcceptance))
    result = unittest.TextTestRunner(verbosity=2).run(suite)
    after = manager_hashes(args.manager_root)
    fixture_after = sha256(args.fixture_exe)
    stable = before == after and fixture_before == fixture_after
    passed = result.wasSuccessful() and result.testsRun > 0 and not result.skipped and stable
    report = {"kind":"real-selectable-channel-acceptance", "testsRun":result.testsRun,
              "failures":len(result.failures), "errors":len(result.errors), "skipped":len(result.skipped),
              "passed":passed, "sourceStable":stable, "fixtureSHA256":fixture_after,
              "managerSourceBefore":before, "managerSourceAfter":after,
              "mysql":"DEFERRED_NOT_PREPARED", "realModels":"NOT_CALLED"}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2)+"\n", encoding="utf-8")
    print(json.dumps({key: value for key, value in report.items() if not key.startswith("managerSource")}))
    return 0 if passed else 1


if __name__ == "__main__":
    raise SystemExit(main())

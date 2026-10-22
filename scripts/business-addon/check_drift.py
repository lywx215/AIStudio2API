"""Run offline synthetic drift fixtures without editing source or Git."""
from __future__ import annotations

import copy
import json
from pathlib import Path
import re
import subprocess
import sys

sys.dont_write_bytecode = True
import check_anchors as gate


def documentation_errors() -> list[str]:
    """Check task bookkeeping and local links, never treat planned tests as runs."""
    board = json.loads((gate.ROOT / "docs/business-addon/tasks.json").read_text(encoding="utf-8"))
    tasks = board["tasks"]
    expected = {f"{prefix}{i:02}" for prefix, begin, end in (
        ("T", 0, 7), ("B", 1, 9), ("C", 1, 7), ("G", 1, 7), ("F", 1, 7),
        ("M", 1, 9), ("Q", 1, 10), ("I", 1, 5), ("S", 1, 3), ("E", 1, 2), ("U", 1, 2),
    ) for i in range(begin, end)}
    ids = [task["id"] for task in tasks]
    errors = []
    if len(tasks) != 58 or set(ids) != expected or len(set(ids)) != len(ids):
        errors.append("tasks.json: expected the original 58 unique R3 task IDs")
    if board.get("live_verified") is not False:
        errors.append("tasks.json: project live_verified must remain false")
    by_id = {task["id"]: task for task in tasks}
    for task in tasks:
        name = task["id"]
        if task["status"] not in board["states"]:
            errors.append(f"{name}: status is absent from the declared states")
        if task.get("live_verified") is not False or task["status"] == "live_verified":
            errors.append(f"{name}: live verification is not authorized or evidenced")
        if any(dependency not in by_id for dependency in task["depends_on"]):
            errors.append(f"{name}: unknown dependency")
        evidence = task.get("verification_evidence", {})
        if "executed_checks" not in evidence or not evidence.get("assessment"):
            errors.append(f"{name}: missing explicit execution/evidence assessment")
        for path in evidence.get("source_paths", []):
            if not (gate.ROOT / path).is_file():
                errors.append(f"{name}: source evidence path does not exist: {path}")
    visiting, visited = set(), set()

    def visit(name: str) -> None:
        if name in visiting:
            errors.append(f"{name}: task dependency cycle")
            return
        if name in visited or name not in by_id:
            return
        visiting.add(name)
        for dependency in by_id[name]["depends_on"]:
            visit(dependency)
        visiting.remove(name)
        visited.add(name)

    for name in by_id:
        visit(name)
    for path in (gate.ROOT / "docs/business-addon").glob("*.md"):
        for target in re.findall(r"\[[^\]]+\]\(([^)]+)\)", path.read_text(encoding="utf-8")):
            if "://" in target or target.startswith("#"):
                continue
            local = target.split("#", 1)[0]
            if local and not (path.parent / local).is_file():
                errors.append(f"{path.name}: missing relative link: {local}")
    return errors


def replace_once(source: str, case: dict) -> str:
    needle = case["find"]
    if source.count(needle) != 1:
        raise ValueError(f"{case['id']}: fixture target must match exactly once")
    return source.replace(needle, case["replace"], 1)


def main() -> int:
    fixture_path = Path(__file__).with_name("fixtures") / "drift.json"
    fixture = json.loads(fixture_path.read_text(encoding="utf-8"))
    if fixture["schema_version"] != 1:
        raise ValueError("unknown drift fixture schema")
    baseline = json.loads(gate.BASELINE.read_text(encoding="utf-8"))
    originals = {p: gate.git("show", f"{gate.EXPECTED_BASE}:{p}") for p in gate.ANCHORS}
    attached = {p: (gate.ROOT / p).read_text(encoding="utf-8") for p in gate.ANCHORS}
    results = []
    seen = set()
    for case in fixture["cases"]:
        if case["id"] in seen:
            raise ValueError("duplicate fixture id")
        seen.add(case["id"])
        kind = case["kind"]
        if kind in ("anchors", "candidate"):
            sources, bases = attached.copy(), originals.copy()
            if "path" in case:
                target = bases if kind == "candidate" else sources
                target[case["path"]] = replace_once(target[case["path"]], case)
            if case.get("crlf"):
                sources = {p: gate.normalized(s).replace("\n", "\r\n") for p, s in sources.items()}
            errors = [e for p in gate.ANCHORS for e in gate.anchor_errors(p, sources[p], bases[p])]
        elif kind == "paths":
            existing, new = gate.unexpected_paths(set(case["tracked"]), set(case["changed"]), set(case["untracked"]))
            errors = existing + new
        elif kind == "baseline":
            modified = copy.deepcopy(baseline)
            modified[case["field"]] = case["value"]
            errors = gate.baseline_errors(modified)
        else:
            raise ValueError(f"unknown fixture kind: {kind}")
        actual_pass = not errors
        results.append({"id": case["id"], "expected_pass": case["expect_pass"], "actual_pass": actual_pass,
                        "matched_expectation": actual_pass == case["expect_pass"]})
    doc_errors = documentation_errors()
    passed = all(r["matched_expectation"] for r in results) and not doc_errors
    print(json.dumps({"passed": passed, "fixtures": len(results), "results": results,
                      "documentation_errors": doc_errors,
                      "real_candidate_verified": False, "live_verified": False,
                      "scope": "synthetic in-memory drift only; no checkout, Go/Web test, browser or network"}, indent=2))
    return 0 if passed else 1


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, KeyError, subprocess.CalledProcessError) as error:
        print(json.dumps({"passed": False, "error": str(error)}))
        sys.exit(1)

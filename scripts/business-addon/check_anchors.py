"""Offline R3 boundary gate; never changes Git or files.

Remove only reviewed attachment statements from four existing anchors, then
require exact baseline source apart from CRLF. Upstream movement fails closed.
"""
from __future__ import annotations

import hashlib
import json
from pathlib import Path
import subprocess
import sys

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[2]
BASELINE = ROOT / "docs/business-addon/baseline.json"
EXPECTED_BASE = "21596862b3805ab15ec184aa0037624c39c07626"
EXPECTED_README = "fa1a7d1a86885e2157fefb04a8332083e94f47eb6120b8ea9df0858b3d8c2f4b"
ANCHORS = (
    "internal/app/app.go", "internal/api/router.go", "web/src/App.vue", "web/src/types.ts",
)
NEW_ROOTS = (
    "internal/addons/business/", "web/src/business/", "docs/business-addon/",
    "scripts/business-addon/", "tests/business-addon/",
)
NEW_BRIDGES = {
    "internal/app/addon_business.go", "internal/app/addon_business_test.go",
    "internal/api/addon_business.go", "internal/api/addon_business_test.go",
}
PREEXISTING_UNTRACKED = (
    "docs/deployment/", "scripts/__pycache__/", "scripts/test_vps_deploy.py", "scripts/vps_deploy.py",
)

# (attached source, original source). Match exactly once, never by line number.
RESTORE = {
    "internal/app/app.go": (
        ("\tbusiness, closeBusiness := newBusinessAddon(manager)\n\tdefer closeBusiness()\n", ""),
        ("\t\tBusiness: business,\n\t\tAPIKey:   cfg.ProxyAPIKey, Admin: manager, Ledger: manager.ledger, Prompts: manager.prompts, Updates: updater,\n",
         "\t\tAPIKey: cfg.ProxyAPIKey, Admin: manager, Ledger: manager.ledger, Prompts: manager.prompts, Updates: updater,\n"),
    ),
    "internal/api/router.go": (
        ("\tBusiness         *BusinessAddon\n", ""),
        ("\t\treturn attachBusiness(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {\n",
         "\t\treturn http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {\n"),
        ("\t\t}), config, consoleAuth)\n\t}\n\treturn attachBusiness(root, config, consoleAuth)\n",
         "\t\t})\n\t}\n\treturn root\n"),
    ),
    "web/src/App.vue": (
        ("import BusinessNavItem from '@/business/BusinessNavItem.vue'\n", ""),
        ("const BusinessPanel = lazyPanel(() => import('@/business/BusinessPanel.vue'))\n", ""),
        ("  'business',\n", ""),
        ('        <BusinessNavItem :active="currentTab === \'business\'" @select="openTab(\'business\')" />\n', ""),
        ('          <BusinessPanel v-else-if="currentTab === \'business\'" />\n', ""),
    ),
    "web/src/types.ts": (("  | 'business'\n", ""),),
}


def normalized(source: str) -> str:
    return source.replace("\r\n", "\n")


def anchor_errors(name: str, attached: str, original: str) -> list[str]:
    restored = normalized(attached)
    for index, (before, after) in enumerate(RESTORE[name], 1):
        if restored.count(before) != 1:
            return [f"{name}: attachment rule {index} must match exactly once"]
        restored = restored.replace(before, after, 1)
    if restored != normalized(original):
        return [f"{name}: source outside the reviewed attachment differs from baseline"]
    return []


def baseline_errors(baseline: dict) -> list[str]:
    failures = []
    expected = {
        "commit": EXPECTED_BASE,
        "existing_anchors": list(ANCHORS),
        "preexisting_tracked_changes": {"README.md": EXPECTED_README},
        "preexisting_untracked_paths": list(PREEXISTING_UNTRACKED),
    }
    for field, value in expected.items():
        if baseline.get(field) != value:
            failures.append(f"baseline.{field}: frozen boundary changed; review required")
    return failures


def unexpected_paths(tracked: set[str], changed: set[str], untracked: set[str]) -> tuple[list[str], list[str]]:
    existing = sorted((changed & tracked) - set(ANCHORS) - {"README.md"})
    unexpected = []
    for name in sorted((changed - tracked) | untracked):
        if name in NEW_BRIDGES or name.startswith(NEW_ROOTS):
            continue
        if name.startswith("web/tests/business-addon") and name.endswith(".test.mjs"):
            continue
        if any(name == old or (old.endswith("/") and name.startswith(old)) for old in PREEXISTING_UNTRACKED):
            continue
        unexpected.append(name)
    return existing, unexpected


def git(*args: str) -> str:
    return subprocess.check_output(["git", *args], cwd=ROOT, text=True, encoding="utf-8")


def git_paths(*args: str) -> set[str]:
    return set(filter(None, git(*args).split("\0")))


def main() -> int:
    baseline = json.loads(BASELINE.read_text(encoding="utf-8"))
    violations = baseline_errors(baseline)
    tracked = git_paths("ls-tree", "-r", "--name-only", "-z", EXPECTED_BASE)
    changed = git_paths("diff", "--name-only", "-z", EXPECTED_BASE, "--")
    untracked = git_paths("ls-files", "--others", "--exclude-standard", "-z")
    existing, new = unexpected_paths(tracked, changed, untracked)
    readme = ROOT / "README.md"
    readme_ok = readme.is_file() and not readme.is_symlink() and hashlib.sha256(readme.read_bytes()).hexdigest() == EXPECTED_README
    if not readme_ok:
        violations.append("README.md: preserved user content hash differs")
    for name in ANCHORS:
        path = ROOT / name
        if not path.is_file() or path.is_symlink():
            violations.append(f"{name}: anchor is absent or a symlink")
            continue
        violations.extend(anchor_errors(name, path.read_text(encoding="utf-8"), git("show", f"{EXPECTED_BASE}:{name}")))
    passed = not violations and not existing and not new
    print(json.dumps({
        "branch": git("branch", "--show-current").strip(),
        "base": EXPECTED_BASE,
        "modified_existing_anchors": sorted(changed & set(ANCHORS)),
        "readme_sha256_preserved": readme_ok,
        "anchor_violations": violations,
        "unexpected_existing_edits": existing,
        "unexpected_new_files": new,
        "passed": passed,
        "scope": "offline edit boundary only; no Go/Web/runtime/live verification",
    }, ensure_ascii=False, indent=2))
    return 0 if passed else 1


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print(json.dumps({"passed": False, "error": str(error)}, ensure_ascii=False))
        sys.exit(1)

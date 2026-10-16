"""Fail closed when the CI inventory drifts from PR workflow triggers."""
import json
import re
from pathlib import Path

import yaml

root = Path(__file__).resolve().parent
config = json.loads((root / "devops-ci.json").read_text(encoding="utf-8"))
declared = {item["path"]: item for item in config["workflows"]}
optional = {item["path"]: item for item in config.get("optionalWorkflows", [])}
assert len(declared) == len(config["workflows"]), "Duplicate workflow paths"
assert len({item["name"] for item in config["workflows"]}) == len(config["workflows"]), "Duplicate workflow names"
assert not set(declared) & set(optional), "Workflow cannot be both required and optional"
actual = {}
mapping = {"paths": "paths", "paths-ignore": "pathsIgnore", "branches": "branches", "branches-ignore": "branchesIgnore"}
for path in sorted((root / "workflows").glob("*")):
    if path.suffix not in {".yml", ".yaml"} or path.name == "devops-gate.yml":
        continue
    data = yaml.safe_load(path.read_text(encoding="utf-8"))
    events = data.get("on", data.get(True, {}))
    if isinstance(events, str):
        events = {events: None}
    elif isinstance(events, list):
        events = dict.fromkeys(events)
    if "pull_request" not in events:
        continue
    name = ".github/workflows/" + path.name
    trigger = events["pull_request"] or {}
    if name in optional:
        assert optional[name].get("reason"), f"Missing optional reason: {name}"
        continue
    types = trigger.get("types", ["opened", "synchronize", "reopened"])
    assert set(["opened", "synchronize", "reopened"]) <= set(types), f"Explicitly classify nonstandard event workflow: {name}"
    item = {"name": data.get("name", path.name), "path": name}
    for source, target in mapping.items():
        if source in trigger:
            item[target] = trigger[source]
            for pattern in trigger[source]:
                assert not any(c in pattern for c in "[]+"), f"Unsupported filter syntax: {name}: {pattern}"
    actual[name] = item
assert set(optional) <= {".github/workflows/" + p.name for p in (root / "workflows").glob("*")}, "Optional workflow does not exist"
assert declared == actual, f"CI inventory drift: update devops-ci.json. Required workflow paths: {sorted(actual)}"

# Privileged aggregation must stay event-driven and execute only default-branch
# code. PR changes to that code are tested in a separate read-only workflow.
gate = yaml.safe_load((root / "workflows/devops-gate.yml").read_text(encoding="utf-8"))
events = gate.get("on", gate.get(True, {}))
assert "pull_request" not in events, "Privileged aggregation must not execute PR code"
assert set(events) == {"pull_request_target", "workflow_run", "workflow_dispatch"}, "Unexpected gate events"
watched = {item["name"] for item in config.get("legacyWorkflows", config["workflows"])}
watched.add(config["orchestrator"]["name"])
assert set(events["workflow_run"]["workflows"]) == watched, "Gate event watcher drift"
assert len(events["workflow_run"]["workflows"]) == len(watched), "Duplicate event watchers"
assert events["workflow_run"]["types"] == ["completed"], "Refresh only completed workflows; early lifecycle events cause runner storms"
assert "edited" in events["pull_request_target"]["types"], "Metadata edits must refresh the gate"
assert events["pull_request_target"]["types"] == ["edited"], "New PR heads must publish only after the unified entry finishes"
assert gate["permissions"] == {"contents": "read"}, "Unexpected global gate permissions"
assert set(gate["jobs"]) == {"refresh-gate"}, "Unexpected privileged gate job"
job = gate["jobs"]["refresh-gate"]
assert job["name"] != "ci-gate", "Helper job must not impersonate the published required check"
assert job["timeout-minutes"] == 5, "Gate must only perform a bounded snapshot"
assert job["permissions"] == {"contents": "read", "pull-requests": "read", "actions": "read", "checks": "write"}, "Unexpected gate permissions"
assert job["concurrency"]["cancel-in-progress"] is False, "Do not cancel a publishing operation"
checkout, publisher = job["steps"]
assert re.fullmatch(r"actions/checkout@[0-9a-f]{40}", checkout["uses"]), "Checkout must be pinned"
assert checkout["with"] == {"ref": "${{ github.event.repository.default_branch }}", "persist-credentials": False, "sparse-checkout": ".github"}, "Privileged checkout must use trusted default branch"
assert re.fullmatch(r"actions/github-script@[0-9a-f]{40}", publisher["uses"]), "Publisher must be pinned"
assert "gate.refresh({ github, context, core, config })" in publisher["with"]["script"], "One-shot publisher is required"
assert "gate-tests" in config["orchestrator"]["needs"], "Gate regression tests must remain mandatory"
assert set(config["orchestrator"]["needs"]) == set(config["orchestrator"]["callers"]) | set(config["orchestrator"]["localNeeds"]), "Caller dependency inventory drift"
assert config.get("eventDrivenGate") is True, "Gate mode must be declared"

parent = yaml.safe_load((root.parent / config["orchestrator"]["path"]).read_text(encoding="utf-8"))
assert parent["name"] == config["orchestrator"]["name"], "Unified entry name drift"
assert parent["permissions"] == {"contents": "read"}, "Unexpected parent permissions"
assert "pull_request" in parent.get("on", parent.get(True, {})), "Unified entry must run on every PR"
summary = parent["jobs"]["ci-results"]
assert summary["if"] == "${{ github.event_name == 'pull_request' && always() }}", "Final result must run even after upstream failures"
assert set(summary["needs"]) == set(config["orchestrator"]["needs"]), "Missing final dependencies"
assert len(summary["needs"]) == len(config["orchestrator"]["needs"]), "Duplicate final dependencies"
assert summary["permissions"] == {"contents": "read"}, "Result summary cannot write checks or execute privileged PR code"
assert summary["timeout-minutes"] == 5, "Result summary must not poll"
assert summary["steps"][-1]["env"] == {"CI_NEEDS": "${{ toJSON(needs) }}"}, "Summary must use scheduler results"
assert re.fullmatch(r"actions/checkout@[0-9a-f]{40}", summary["steps"][0]["uses"]), "Summary checkout must be pinned"
assert summary["steps"][0]["with"]["persist-credentials"] is False, "Read-only summary must not persist credentials"
assert "verifyNeeds(JSON.parse(process.env.CI_NEEDS), config.orchestrator.needs)" in summary["steps"][-1]["run"], "Final result validator is required"
for name, path in config["orchestrator"]["callers"].items():
    caller = parent["jobs"][name]
    assert caller["uses"] == "./" + path, f"Required reusable workflow replaced: {name}"
    assert caller["if"] == "github.event_name == 'pull_request'", f"Required PR caller skipped: {name}"
    expected_permissions = {"contents": "read", "pull-requests": "read", "actions": "read"} if name == "gate-tests" else {"contents": "read", "security-events": "write"}
    assert caller["permissions"] == expected_permissions, f"Unexpected reusable caller permissions: {name}"
    child = yaml.safe_load((root.parent / path).read_text(encoding="utf-8"))
    child_events = child.get("on", child.get(True, {}))
    assert "workflow_call" in child_events and "pull_request" not in child_events, "Independent PR triggers duplicate the unified run"
for name in config["orchestrator"]["localNeeds"]:
    assert name in parent["jobs"], f"Missing original local check: {name}"

# Required status contexts match exact check names, not suffixes. Keep the
# existing audit at the parent level; do not replace it with a passing alias.
assert "dependency-audit" in config["orchestrator"]["localNeeds"], "Required dependency audit missing from summary"
audit = parent["jobs"]["dependency-audit"]
assert audit.get("name", "dependency-audit") == "dependency-audit", "Required dependency-audit check renamed"
assert "uses" not in audit and "needs" not in audit, "Required audit must run directly and independently"
assert audit["if"] == "github.event_name == 'pull_request'", "Required audit must run on every PR"
assert audit.get("permissions", parent["permissions"]) == {"contents": "read"}, "Audit must remain read-only"
assert audit["runs-on"] == "ubuntu-latest", "Unexpected audit runner"
security = yaml.safe_load((root / "workflows/security.yml").read_text(encoding="utf-8"))
scheduled_audit = security["jobs"]["dependency-audit"]
assert scheduled_audit["if"] == "github.event_name != 'pull_request'", "PR audit must execute only once"
assert audit["steps"] == scheduled_audit["steps"], "PR and scheduled audit coverage drift"
assert [step.get("run") for step in audit["steps"] if "run" in step] == ["npm ci", "npm run security:check"], "Required audit must run the real security scan"

# Explicit SARIF categories must match the existing main baselines. Default
# categories change with reusable callers and break introduced-alert comparison.
for filename, name, languages, category in [
    ("codeql.yml", "analyze", {"go", "actions"}, ".github/workflows/codeql.yml:analyze/language:${{ matrix.language }}"),
    ("security.yml", "codeql", {"javascript-typescript"}, "/language:${{ matrix.language }}"),
]:
    workflow = yaml.safe_load((root / "workflows" / filename).read_text(encoding="utf-8"))
    scan = workflow["jobs"][name]
    assert set(scan["strategy"]["matrix"]["language"]) == languages, f"CodeQL language coverage drift: {filename}"
    init, build, analyze = scan["steps"][1:]
    assert init["uses"] == "github/codeql-action/init@v3" and init["with"]["languages"] == "${{ matrix.language }}", "CodeQL initialization must cover each language"
    assert build["uses"] == "github/codeql-action/autobuild@v3", "CodeQL build must remain enabled"
    assert analyze["uses"] == "github/codeql-action/analyze@v3", "CodeQL analysis must remain enabled"
    assert analyze.get("with") == {"category": category}, f"CodeQL baseline category drift or upload disabled: {filename}"
tests = yaml.safe_load((root / "workflows/devops-gate-tests.yml").read_text(encoding="utf-8"))
assert tests["permissions"] == {"contents": "read"}, "PR regression tests must have read-only permissions"
assert "permissions" not in tests["jobs"]["gate-tests"], "PR tests must not elevate permissions"
bootstrap = tests["jobs"]["bootstrap-gate"]
assert bootstrap["permissions"] == {"contents": "read", "pull-requests": "read", "actions": "read"}, "Legacy bootstrap must remain read-only"
assert bootstrap["if"] == "github.event_name == 'pull_request' && needs.gate-tests.outputs.bootstrap == 'true'", "Legacy gate must stop after installation"
assert bootstrap["name"] == "${{ needs.gate-tests.outputs.bootstrap == 'true' && 'ci-gate' || 'gate-bootstrap-inactive' }}", "Inactive bootstrap must not create a required-name skipped check"
assert bootstrap["steps"][0]["with"]["ref"] == "${{ github.event.repository.default_branch }}", "Legacy bootstrap must use deployed trusted code"
print(f"PASS: {len(actual)} required PR workflows, {len(optional)} explicit optional workflows")

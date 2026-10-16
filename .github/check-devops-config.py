"""Fail closed when the PR CI inventory drifts from workflow triggers."""
import json
from pathlib import Path

import yaml

root = Path(__file__).resolve().parent
config = json.loads((root / "devops-ci.json").read_text(encoding="utf-8"))
declared = {item["path"]: item for item in config["workflows"]}
assert len(declared) == len(config["workflows"]), "Duplicate workflow paths"
assert len({item["name"] for item in config["workflows"]}) == len(config["workflows"]), "Duplicate workflow names"
actual = {}
mapping = {"paths": "paths", "paths-ignore": "pathsIgnore", "branches": "branches", "branches-ignore": "branchesIgnore"}
for path in sorted((root / "workflows").glob("*")):
    if path.suffix not in {".yml", ".yaml"}:
        continue
    if path.name in {"devops-gate.yml", "codeql.yml", "security.yml", "devops-gate-tests.yml"}:
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
    types = trigger.get("types", ["opened", "synchronize", "reopened"])
    assert set(["opened", "synchronize", "reopened"]) <= set(types), f"Explicitly classify nonstandard event workflow: {name}"
    item = {"name": data.get("name", path.name), "path": name}
    for source, target in mapping.items():
        if source in trigger:
            item[target] = trigger[source]
            for pattern in trigger[source]:
                assert not any(c in pattern for c in "[]+"), f"Unsupported filter syntax: {name}: {pattern}"
    actual[name] = item
assert declared == actual, f"CI inventory drift: update devops-ci.json. Required workflow paths: {sorted(actual)}"

parent = yaml.safe_load((root.parent / config["workflows"][0]["path"]).read_text(encoding="utf-8"))
assert parent["name"] == config["workflows"][0]["name"], "Unified entry name drift"
assert parent["permissions"] == {"contents": "read"}, "Unexpected parent permissions"
assert "pull_request" in parent.get("on", parent.get(True, {}))
gate = parent["jobs"]["ci-gate"]
assert gate.get("name") == "ci-gate", "Branch protection requires the ci-gate job name"
assert gate["if"] == "${{ always() }}", "Aggregate gate must observe skipped/failed upstream jobs"
required = {"e2e-plan", "source-lines", "sdk-gate", "control-service", "example-agent", "compose-e2e", "data-plane-kubernetes", "backup-restore"}
assert set(gate["needs"]) == required, "Aggregate gate dependency drift"
assert "go test ./... -race" in yaml.safe_dump(parent), "PR Go verification must keep the race detector"
print(f"PASS: {len(actual)} required PR workflows")

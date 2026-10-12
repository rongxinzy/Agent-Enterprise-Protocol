"""Regression-test the CI inventory and privileged workflow boundary."""
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

import yaml


class WorkflowBoundaryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="devops-gate-policy-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / ".github"
        shutil.copytree(Path(__file__).resolve().parent, self.root,
                        ignore=shutil.ignore_patterns("__pycache__"))

    def check(self):
        return subprocess.run([sys.executable, str(self.root / "check-devops-config.py")],
                              capture_output=True, text=True, encoding="utf-8", check=False)

    def mutate(self, filename, callback):
        path = self.root / filename
        data = yaml.safe_load(path.read_text(encoding="utf-8"))
        callback(data)
        path.write_text(yaml.safe_dump(data), encoding="utf-8")

    def test_current_workflows_pass(self):
        result = self.check()
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_privileged_pr_checkout_is_rejected(self):
        self.mutate("workflows/devops-gate.yml", lambda data:
                    data["jobs"]["refresh-gate"]["steps"][0]["with"].update(ref="${{ github.event.pull_request.head.sha }}"))
        self.assertNotEqual(self.check().returncode, 0)

    def test_extra_privileged_execution_is_rejected(self):
        self.mutate("workflows/devops-gate.yml", lambda data:
                    data["jobs"]["refresh-gate"]["steps"].append({"run": "node untrusted.js"}))
        self.assertNotEqual(self.check().returncode, 0)

    def test_missing_completion_watcher_is_rejected(self):
        self.mutate("workflows/devops-gate.yml", lambda data:
                    data[True]["workflow_run"].update(types=["requested", "in_progress"]))
        self.assertNotEqual(self.check().returncode, 0)

    def test_missing_security_workflow_watcher_is_rejected(self):
        self.mutate("workflows/devops-gate.yml", lambda data:
                    data[True]["workflow_run"]["workflows"].remove("CodeQL"))
        self.assertNotEqual(self.check().returncode, 0)

    def test_pr_test_write_permission_is_rejected(self):
        self.mutate("workflows/devops-gate-tests.yml", lambda data:
                    data["permissions"].update(checks="write"))
        self.assertNotEqual(self.check().returncode, 0)

    def test_legacy_bootstrap_write_permission_is_rejected(self):
        self.mutate("workflows/devops-gate-tests.yml", lambda data:
                    data["jobs"]["bootstrap-gate"]["permissions"].update(checks="write"))
        self.assertNotEqual(self.check().returncode, 0)

    def test_inactive_legacy_required_name_is_rejected(self):
        self.mutate("workflows/devops-gate-tests.yml", lambda data:
                    data["jobs"]["bootstrap-gate"].update(name="ci-gate"))
        self.assertNotEqual(self.check().returncode, 0)

    def test_unpinned_privileged_action_is_rejected(self):
        self.mutate("workflows/devops-gate.yml", lambda data:
                    data["jobs"]["refresh-gate"]["steps"][1].update(uses="actions/github-script@main"))
        self.assertNotEqual(self.check().returncode, 0)

    def test_global_write_permission_is_rejected(self):
        self.mutate("workflows/devops-gate.yml", lambda data:
                    data["permissions"].update(contents="write"))
        self.assertNotEqual(self.check().returncode, 0)

    def test_trigger_inventory_drift_is_rejected(self):
        self.mutate("workflows/codeql.yml", lambda data:
                    data[True].update(pull_request={"paths": ["docs/**"]}))
        self.assertNotEqual(self.check().returncode, 0)

    def test_duplicate_names_are_rejected(self):
        path = self.root / "devops-ci.json"
        data = json.loads(path.read_text(encoding="utf-8"))
        data["workflows"][1]["name"] = data["workflows"][0]["name"]
        path.write_text(json.dumps(data), encoding="utf-8")
        self.assertNotEqual(self.check().returncode, 0)


if __name__ == "__main__":
    unittest.main()

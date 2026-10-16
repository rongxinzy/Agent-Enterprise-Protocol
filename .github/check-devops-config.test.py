"""Regression-test the PR CI inventory."""
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import yaml


class WorkflowBoundaryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="devops-ci-policy-")
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
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)

    def test_missing_aggregate_dependency_is_rejected(self):
        self.mutate("workflows/m0.yml", lambda data: data["jobs"]["ci-gate"]["needs"].remove("sdk-gate"))
        self.assertNotEqual(self.check().returncode, 0)

    def test_failure_skipping_aggregate_gate_is_rejected(self):
        self.mutate("workflows/m0.yml", lambda data: data["jobs"]["ci-gate"].update({"if": "success()"}))
        self.assertNotEqual(self.check().returncode, 0)

    def test_race_detector_removal_is_rejected(self):
        self.mutate("workflows/m0.yml", lambda data:
                    data["jobs"]["control-service"]["steps"][-1].update({"run": "go test ./..."}))
        self.assertNotEqual(self.check().returncode, 0)


if __name__ == "__main__":
    unittest.main()

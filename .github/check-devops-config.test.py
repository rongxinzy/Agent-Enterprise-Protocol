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

    def test_early_lifecycle_refresh_storm_is_rejected(self):
        self.mutate("workflows/devops-gate.yml", lambda data:
                    data[True]["workflow_run"].update(types=["requested", "in_progress", "completed"]))
        self.assertNotEqual(self.check().returncode, 0)

    def parent_path(self):
        config = json.loads((self.root / "devops-ci.json").read_text(encoding="utf-8"))
        return config["orchestrator"]["path"].removeprefix(".github/")

    def test_missing_final_dependency_is_rejected(self):
        self.mutate(self.parent_path(), lambda data:
                    data["jobs"]["ci-results"]["needs"].remove("codeql"))
        self.assertNotEqual(self.check().returncode, 0)

    def test_failure_skipping_final_gate_is_rejected(self):
        self.mutate(self.parent_path(), lambda data:
                    data["jobs"]["ci-results"].update({"if": "success()"}))
        self.assertNotEqual(self.check().returncode, 0)

    def test_reusable_security_check_cannot_be_disabled(self):
        self.mutate(self.parent_path(), lambda data:
                    data["jobs"]["codeql"].update({"if": "false"}))
        self.assertNotEqual(self.check().returncode, 0)

    def test_direct_pr_trigger_cannot_duplicate_reusable_checks(self):
        self.mutate("workflows/codeql.yml", lambda data:
                    data[True].update(pull_request=None))
        self.assertNotEqual(self.check().returncode, 0)

    def test_final_pr_summary_cannot_gain_write_permissions(self):
        self.mutate(self.parent_path(), lambda data:
                    data["jobs"]["ci-results"]["permissions"].update(checks="write"))
        self.assertNotEqual(self.check().returncode, 0)

    def test_extra_initial_gate_runner_is_rejected(self):
        self.mutate("workflows/devops-gate.yml", lambda data:
                    data[True]["pull_request_target"].update(types=["opened", "edited", "synchronize"]))
        self.assertNotEqual(self.check().returncode, 0)

    def test_single_entry_preserves_original_local_checks(self):
        config = json.loads((self.root / "devops-ci.json").read_text(encoding="utf-8"))
        parent = yaml.safe_load((self.root.parent / config["orchestrator"]["path"]).read_text(encoding="utf-8"))
        self.assertEqual(len(config["workflows"]), 1)
        self.assertEqual(parent["concurrency"]["cancel-in-progress"], True)
        for name in config["orchestrator"]["localNeeds"]:
            self.assertIn(name, parent["jobs"])

    def test_required_dependency_audit_runs_under_original_name(self):
        parent = yaml.safe_load((self.root / self.parent_path()).read_text(encoding="utf-8"))
        self.assertIn("dependency-audit", parent["jobs"],
                      "Nested security / dependency-audit cannot satisfy the required dependency-audit check")
        audit = parent["jobs"]["dependency-audit"]
        self.assertEqual(audit.get("name", "dependency-audit"), "dependency-audit")
        self.assertEqual(audit["if"], "github.event_name == 'pull_request'")
        self.assertNotIn("uses", audit)
        security = yaml.safe_load((self.root / "workflows/security.yml").read_text(encoding="utf-8"))
        self.assertEqual(audit["steps"], security["jobs"]["dependency-audit"]["steps"])
        self.assertIn("dependency-audit", parent["jobs"]["ci-results"]["needs"])

    def test_codeql_categories_match_existing_main_baselines(self):
        workflow = yaml.safe_load((self.root / "workflows/codeql.yml").read_text(encoding="utf-8"))
        job = workflow["jobs"]["analyze"]
        self.assertEqual(set(job["strategy"]["matrix"]["language"]), {"go", "actions"})
        analyze = job["steps"][-1]
        self.assertEqual(analyze["uses"], "github/codeql-action/analyze@v3")
        self.assertEqual(analyze.get("with", {}).get("category"),
                         ".github/workflows/codeql.yml:analyze/language:${{ matrix.language }}",
                         "Reusable caller path must not replace main's original scan categories")

    def test_codeql_default_caller_category_is_rejected(self):
        self.mutate("workflows/codeql.yml", lambda data:
                    data["jobs"]["analyze"]["steps"][-1].pop("with"))
        self.assertNotEqual(self.check().returncode, 0)

    def test_codeql_caller_path_category_is_rejected(self):
        self.mutate("workflows/codeql.yml", lambda data:
                    data["jobs"]["analyze"]["steps"][-1]["with"].update(
                        category=".github/workflows/m0.yml:analyze/language:${{ matrix.language }}"))
        self.assertNotEqual(self.check().returncode, 0)

    def test_codeql_language_removal_is_rejected(self):
        self.mutate("workflows/codeql.yml", lambda data:
                    data["jobs"]["analyze"]["strategy"]["matrix"].update(language=["go"]))
        self.assertNotEqual(self.check().returncode, 0)

    def test_codeql_disabled_upload_is_rejected(self):
        self.mutate("workflows/codeql.yml", lambda data:
                    data["jobs"]["analyze"]["steps"][-1]["with"].update(upload="never"))
        self.assertNotEqual(self.check().returncode, 0)

    def test_security_codeql_category_collision_is_rejected(self):
        self.mutate("workflows/security.yml", lambda data:
                    data["jobs"]["codeql"]["steps"][-1]["with"].update(
                        category=".github/workflows/codeql.yml:analyze/language:${{ matrix.language }}"))
        self.assertNotEqual(self.check().returncode, 0)

    def test_dependency_audit_renamed_check_is_rejected(self):
        self.mutate(self.parent_path(), lambda data:
                    data["jobs"]["dependency-audit"].update(name="security / dependency-audit"))
        self.assertNotEqual(self.check().returncode, 0)

    def test_dependency_audit_skipping_prs_is_rejected(self):
        self.mutate(self.parent_path(), lambda data:
                    data["jobs"]["dependency-audit"].update({"if": "false"}))
        self.assertNotEqual(self.check().returncode, 0)

    def test_dependency_audit_fake_passing_alias_is_rejected(self):
        self.mutate(self.parent_path(), lambda data:
                    data["jobs"]["dependency-audit"].update(steps=[{"run": "true"}]))
        self.assertNotEqual(self.check().returncode, 0)

    def test_dependency_audit_scan_cannot_be_weakened_in_both_workflows(self):
        def weaken(data):
            data["jobs"]["dependency-audit"]["steps"][-1]["run"] = "echo security passed"
        self.mutate(self.parent_path(), weaken)
        self.mutate("workflows/security.yml", weaken)
        self.assertNotEqual(self.check().returncode, 0)

    def test_dependency_audit_scheduled_coverage_drift_is_rejected(self):
        self.mutate("workflows/security.yml", lambda data:
                    data["jobs"]["dependency-audit"]["steps"].pop())
        self.assertNotEqual(self.check().returncode, 0)

    def test_duplicate_pr_dependency_audit_is_rejected(self):
        self.mutate("workflows/security.yml", lambda data:
                    data["jobs"]["dependency-audit"].pop("if"))
        self.assertNotEqual(self.check().returncode, 0)

    def test_dependency_audit_missing_from_all_summary_inventory_is_rejected(self):
        self.mutate(self.parent_path(), lambda data:
                    data["jobs"]["ci-results"]["needs"].remove("dependency-audit"))
        path = self.root / "devops-ci.json"
        config = json.loads(path.read_text(encoding="utf-8"))
        config["orchestrator"]["localNeeds"].remove("dependency-audit")
        config["orchestrator"]["needs"].remove("dependency-audit")
        path.write_text(json.dumps(config), encoding="utf-8")
        self.assertNotEqual(self.check().returncode, 0)

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
        data["workflows"].append({**data["workflows"][0], "path": ".github/workflows/duplicate.yml"})
        path.write_text(json.dumps(data), encoding="utf-8")
        self.assertNotEqual(self.check().returncode, 0)


if __name__ == "__main__":
    unittest.main()

# Pull request E2E selection

The `CI validation` workflow runs for every PR. SDK/contract checks, Go unit coverage,
race/vet/build, reference-Agent tests and the coverage ratchet remain required.
CodeQL and the security baseline keep their existing triggers and checks.
Only the expensive Compose, Kubernetes and backup E2E jobs use scoped selection.

## Selection policy

`.github/devops-ci.json` records the scenario inventory and dependency mapping.
The planner compares the PR head with its base using the merge-base three-dot
diff, including deleted paths and both sides of renames. Multiple scopes select
the union of their scenarios, executed in the existing canonical order.

| Changed area | Compose scenarios | Other E2E |
| --- | --- | --- |
| Markdown documentation only | None | None |
| Gateway authorizer / shared gateway policy / mock provider | m1-control, m1-gateway, gateway-native, m1-client, m3-data-plane | Kubernetes |
| Gateway reconciler / Higress fixtures | gateway-native, m3-data-plane | Kubernetes |
| Model management / model gateway APIs | m1-control, m1-gateway, gateway-native, m1-client, m2-control, m3-data-plane | Kubernetes |
| Credential implementation | m1-control, m1-gateway, gateway-native, m1-client, m2-control, m2-agent, m3-data-plane | Kubernetes |
| Skill APIs / blob storage | m0, m2-control, m2-agent, runtime, upgrade | Backup/restore |
| Reference Node Agent | m0, m1-client, m2-agent | None |
| Event / telemetry / audit / retention APIs | m0, m1-client, m2-control, m2-agent, runtime, upgrade | None |
| Backup/restore implementation | None | Backup/restore |
| One existing E2E scenario script only | That scenario | Kind or backup when editing their script |
| Authentication, licenses, migrations, common configuration, SDK, OpenAPI, dependencies, CI, or any unmapped path | All ten default Compose scenarios | Kubernetes and backup/restore |

New code paths and shared test helpers are deliberately unmapped until reviewed.
They run all E2E rather than guessing a dependency. Missing Git history or an
invalid/incomplete changed-file inventory also falls back to all E2E. This
policy is conservative, not a proof that directory boundaries fully describe
runtime dependencies; update mappings when dependencies or scenarios change.

## Gate and release guarantees

`release-gate` always evaluates job results. Prerequisites must succeed; each
selected E2E job must succeed; each unselected E2E job must be explicitly skipped.
Failure, cancellation, a missing plan or an unexpected skip fails the aggregate.
Malformed plans and unknown scenario IDs cannot launch arbitrary commands.

Main/other configured pushes and manual `workflow_dispatch` run the full plan.
The existing `npm run test:e2e`, `npm run release:check`, tag-release workflow
and local-only offline-license test boundary are unchanged. The job summary
lists the selected checks. This does not change PR base rules, branch protection,
approval requirements, or the separate `ci-gate` timeout/polling implementation.
It reduces E2E work for scoped PRs; runner queue time can still cause a timeout.

## Local verification

```bash
node --test .github/e2e-plan.test.cjs
python .github/check-devops-config.py
AEP_CI_BASE_SHA=<base-commit> AEP_CI_HEAD_SHA=<head-commit> GITHUB_EVENT_NAME=pull_request node .github/e2e-plan.cjs
AEP_E2E_PLAN='{"compose":["m1-control"],"kubernetes":false,"backup":false}' node .github/run-selected-e2e.cjs
```

Review the mapping and workflow graph together when changing this policy.
CI and gate changes require maintainer review under [DEVOPS.md](../DEVOPS.md).

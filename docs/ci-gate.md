# CI gate rollout and queue control

## Completion-only refresh and unified entry rollout

Gate refreshes now watch completion events only. Requested and in-progress
events do not allocate additional runners. Metadata edits and manual recovery
remain supported; neither can waive a failed or missing check.

Roll out in two PRs, in order:

1. Deploy the completion-only trusted publisher and its compatibility rules.
   Existing PR heads still require every applicable independent workflow.
2. Convert those checks into reusable workflows called by `CI validation`.
   Its final `ci-results` job uses `needs` and `always()` to reject failures,
   cancellations, missing dependencies and unexpected skips. The trusted
   publisher then takes one final snapshot on entry completion.

The compatibility publisher accepts only an authoritative current-head run
of the configured entry. Independently started legacy checks on that same
head also remain required. An old PR does not need a mechanical main update:
its independent workflows continue to be evaluated.

This avoids an installation deadlock: a publisher on main that knows only old
workflow paths cannot accept a migration PR after those triggers are removed.
Do not merge stage 2 before stage 1 is deployed. Each repository rolls out
independently. Existing queued runs are not removed by merging the change;
runner availability can still delay the single final publisher.

## Required check

The unified entry retains the original top-level business jobs, including
Admin Console's required `verify` name. CodeQL, gate regression tests and AEP
security checks are called concurrently as reusable workflows. They retain
their own scheduled/main/manual triggers, but no longer start independently
on each PR. A new PR head triggers no initial privileged publisher; only
the parent completion triggers publication. Title/body/base edits and manual
recovery still revalidate current metadata.

AEP's dependency audit is a top-level `dependency-audit` job in the unified
entry. GitHub branch protection matches exact check names: the nested
`security / dependency-audit` name cannot satisfy the existing required
`dependency-audit` context. The top-level job runs the same `npm ci` and
`npm run security:check` steps as the scheduled security workflow and is
required by `ci-results`. The reusable security workflow skips its duplicate
audit only for PR events; main, scheduled and manual audits remain enabled.
Policy tests reject renaming, skipping, removing the real scan, audit drift
or removing the audit from final aggregation.

For PR heads created before this repair, rerunning their existing workflow
uses their old YAML and cannot restore the missing check name. After the
repair merges, merge the updated main into an affected PR once and run CI
on the new head. This is an actual CI configuration dependency; metadata
refreshes alone cannot supply the missing audit. Preserve required checks
and obtain approval for the updated head under the existing review policy.

The final read-only `ci-results` job depends on all required caller results.
AEP's existing `release-gate` continues to check its full business/E2E DAG,
including precisely permitted scoped skips. The final publisher remains
default-branch-only and never executes PR code or artifacts with write access.
The last publication still needs one runner: this change removes redundant
refresh launches, not GitHub-hosted runner capacity limits.

`ci-gate` remains the required GitHub Actions check. Its status is now published
through the Checks API, separately from the short `refresh-ci-gate` helper job:

| Current PR state | Required `ci-gate` result |
| --- | --- |
| An applicable workflow is missing, queued or running | Queued, without a passing conclusion |
| All applicable workflows and PR metadata pass | Success |
| Metadata validation or an applicable workflow fails/cancels/times out/skips | Failure |
| Head, base branch, metadata or open state changes during aggregation | Cancelled snapshot; the new event evaluates the current state |

The helper takes one API snapshot and exits. It never sleeps while another
workflow waits for a runner. PR metadata edits and required workflow completion
events refresh the check. Runner congestion may delay a
refresh, but upstream queue time is no longer a 30-minute gate failure.

Repeated refreshes update the publisher-owned check for the same PR and head;
they do not accumulate duplicate queued checks or edit checks from other apps.
Reruns reset that check to queued before evaluating current results.

## Trust and validation

The publisher runs on `pull_request_target` / `workflow_run`, with only repository
read permissions plus `checks: write`. It checks out `.github` from the trusted
default branch. It never executes PR code, consumes upstream artifacts or uses
user-supplied shell commands. The helper's own success does not satisfy the
required check: its name is `refresh-ci-gate`, not `ci-gate`.

PR code for the gate is tested in `DevOps gate tests`, with read-only permissions.
That reusable workflow is required through the unified entry; its former
independent name remains watched for old PR heads. The
inventory test checks triggers, default-branch checkout, permissions, pinned
actions, watcher completeness and the bootstrap boundary.

Aggregation fetches current PR metadata, requires a complete paginated changed
file list, includes old rename paths, and applies the recorded workflow filters.
Only current-head `pull_request` runs with the correct workflow path, PR number,
base branch, head repository and branch can pass. The newest run/attempt wins;
historical successes, cancelled runs, pushes and unrelated PRs cannot satisfy
the gate. It rechecks metadata immediately before publishing the result.

Missing API association is kept pending, including fork runs with an empty
`pull_requests` list; matching open PRs are resolved read-only, but association
is not invented. API/write errors cannot turn a pending check green. Required
source checks, coverage, security, review and branch protection remain intact.

## Installation and recovery

The event publisher becomes active only after this change reaches the default
branch. To allow that installation PR to pass the existing required check, the
read-only test workflow reads the deployed `eventDrivenGate` flag and runs the
old trusted default-branch gate only while the flag is absent. That one-time
bootstrap still uses the old polling/timeout behavior; if it times out, finish
the upstream CI and rerun its failed job. It does not waive any checks.

After installation, the bootstrap is skipped under the different name
`gate-bootstrap-inactive`, so its skipped result cannot impersonate `ci-gate`.
New changes use event-driven aggregation. Existing PRs do not need new source
commits merely to trigger a refresh: reopen them to start the newly introduced
read-only test workflow, or use its existing rerun once present. A missing
required test workflow must actually run before the gate can succeed.

To recover from a missed/delayed event, dispatch `DevOps gate` on the default
branch with an open PR number. This only recomputes the check from current APIs;
it does not restart other workflows, alter reviews or update PR branches. Do
not use it to bypass a real failed or missing upstream check. Same-head reruns
are re-evaluated when their lifecycle events arrive; manual refresh is available
while waiting for an event to be delivered.

## Local checks

```bash
node --test .github/devops-gate.test.cjs
python .github/check-devops-config.py
python .github/check-devops-config.test.py
```

CI policy changes still require maintainer review under [DEVOPS.md](../DEVOPS.md).

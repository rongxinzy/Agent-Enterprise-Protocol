'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { run, eventPullNumbers } = require('./devops-gate.cjs');

function fixture() {
  const pr = {
    number: 7, state: 'open', title: 'ci: consolidate checks',
    body: '## 改动\n统一入口。\n## 原因\n减少排队。\n## 验证\n测试通过。',
    changed_files: 1, head: { sha: 'abc', ref: 'feature', repo: { id: 10 } }, base: { ref: 'main' },
  };
  const legacy = { name: 'Security', path: '.github/workflows/security.yml' };
  const orchestrator = { name: 'CI validation', path: '.github/workflows/ci-validation.yml' };
  const state = { runs: [] };
  const workflowRun = (workflow, overrides = {}) => ({
    id: 1, name: workflow.name, event: 'pull_request', head_sha: pr.head.sha, head_branch: pr.head.ref,
    head_repository: { id: 10 }, path: workflow.path, status: 'completed', conclusion: 'success',
    pull_requests: [{ number: 7, head: { sha: 'abc' }, base: { ref: 'main' } }], ...overrides,
  });
  const github = {
    rest: { pulls: { get: async () => ({ data: pr }), listFiles: 'files' },
      actions: { listWorkflowRunsForRepo: 'runs', getWorkflowRun: async () => ({ data: state.runs[0] }) } },
    paginate: async (endpoint) => endpoint === 'files' ? [{ filename: 'source.go' }] : state.runs,
  };
  const config = { workflows: [legacy], orchestrator };
  const context = { repo: { owner: 'owner', repo: 'repo' }, payload: { pull_request: pr } };
  return { state, workflowRun, legacy, orchestrator, github, config, context, core: { info() {} } };
}

test('legacy heads still require all legacy checks; unified heads require the complete entry', async () => {
  const f = fixture();
  assert.equal((await run(f)).status, 'queued');
  f.state.runs = [f.workflowRun(f.legacy)];
  assert.equal((await run(f)).conclusion, 'success');
  f.state.runs = [f.workflowRun(f.orchestrator)];
  assert.equal((await run(f)).conclusion, 'success');
  f.state.runs[0].status = 'in_progress';
  assert.equal((await run(f)).status, 'queued');
  f.state.runs[0].status = 'completed';
  for (const conclusion of ['failure', 'cancelled', 'skipped']) {
    f.state.runs[0].conclusion = conclusion;
    await assert.rejects(run(f), /CI validation/);
  }
});

test('unified entry cannot hide a parallel legacy failure or be spoofed by another run', async () => {
  const f = fixture();
  const unified = f.workflowRun(f.orchestrator);
  f.state.runs = [unified, f.workflowRun(f.legacy, { id: 2, conclusion: 'failure' })];
  await assert.rejects(run(f), /Security: failure/);
  for (const overrides of [
    { head_sha: 'old' }, { event: 'push' }, { head_repository: { id: 99 } },
    { head_branch: 'other' }, { path: '.github/workflows/other.yml' }, { pull_requests: [] },
    { pull_requests: [{ number: 8, head: { sha: 'abc' }, base: { ref: 'main' } }] },
  ]) {
    f.state.runs = [{ ...unified, ...overrides }];
    assert.equal((await run(f)).status, 'queued');
  }
  f.state.runs = [unified, { ...unified, id: 2, run_attempt: 2, conclusion: 'failure' }];
  await assert.rejects(run(f), /CI validation: failure/);
});

test('completion of the new entry resolves only the current associated PR', async () => {
  const f = fixture();
  f.state.runs = [f.workflowRun(f.orchestrator)];
  f.context.eventName = 'workflow_run';
  f.context.payload = { workflow_run: { id: 1 } };
  assert.deepEqual(await eventPullNumbers(f), [7]);
  f.state.runs[0].head_sha = 'old';
  assert.deepEqual(await eventPullNumbers(f), []);
});

test('renaming the existing entry does not make old heads skip the other checks', async () => {
  const f = fixture();
  f.config.workflows = [f.legacy, { name: 'Old CI', path: f.orchestrator.path }];
  f.state.runs = [f.workflowRun({ name: 'Old CI', path: f.orchestrator.path })];
  assert.equal((await run(f)).status, 'queued', 'legacy security must still run');
  f.state.runs = [f.workflowRun(f.orchestrator)];
  assert.equal((await run(f)).conclusion, 'success');
});

test('after rollout, legacy head results and completion events remain valid', async () => {
  const f = fixture();
  f.config.legacyWorkflows = [f.legacy];
  f.config.workflows = [f.orchestrator];
  f.state.runs = [f.workflowRun(f.legacy)];
  assert.equal((await run(f)).conclusion, 'success');
  f.context.eventName = 'workflow_run';
  f.context.payload = { workflow_run: { id: 1 } };
  assert.deepEqual(await eventPullNumbers(f), [7]);
});

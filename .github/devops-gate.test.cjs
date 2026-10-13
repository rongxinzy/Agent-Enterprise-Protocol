'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { validatePr, workflowApplies, evaluateRuns, run, refresh, eventPullNumbers } = require('./devops-gate.cjs');
const body =
  '## 改动\n保留更新请求未携带的字段。\n## 原因\n触发：只改名。原因：旧实现清空缺席字段。修复前丢配置，修复后保留。\n## 验证\n接口回归测试通过，尚未部署。';
test('valid bug description passes; missing trigger and blank template fail', () => {
  assert.deepEqual(validatePr({ title: 'fix(portal): preserve omitted fields', body }), []);
  assert.ok(validatePr({ title: 'update', body }).length);
  assert.ok(validatePr({ title: 'fix: preserve fields', body: body.replace('触发', '情形') }).length);
  assert.ok(validatePr({ title: 'fix: preserve fields', body: body.replace('原因：旧实现清空缺席字段。', '') }).length);
  assert.ok(
    validatePr({ title: 'docs: update conventions', body: '## 改动\n<!-- done -->\n## 原因\nTBD\n## 验证\n-\n' })
      .length,
  );
});
test('aggregation fails closed for failed CI, changed head and incomplete file lists', async () => {
  const pr = {
    number: 1,
    title: 'ci: enforce gates',
    body,
    changed_files: 1,
    state: 'open',
    head: { sha: 'abc', ref: 'change', repo: { id: 1 } },
    base: { ref: 'main' },
  };
  const options = {
    context: { repo: { owner: 'example', repo: 'example' }, payload: { pull_request: pr } },
    core: { info() {} },
    config: { workflows: [{ name: 'CI', path: '.github/workflows/ci.yml' }] },
    sleep: async () => {},
  };
  const github = {
    rest: {
      pulls: { listFiles: 'files', get: async () => ({ data: pr }) },
      actions: { listWorkflowRunsForRepo: 'runs' },
    },
    paginate: async (endpoint) =>
      endpoint === 'files'
        ? [{ filename: 'main.go' }]
        : [
            {
              id: 1,
              path: '.github/workflows/ci.yml',
              event: 'pull_request',
              head_sha: 'abc',
              status: 'completed',
              conclusion: 'failure',
              head_repository: { id: 1 },
              head_branch: 'change',
              pull_requests: [{ number: 1, head: { sha: 'abc' }, base: { ref: 'main' } }],
            },
          ],
  };
  await assert.rejects(run({ ...options, github }), /CI: failure/);
  await assert.rejects(
    run({
      ...options,
      github: {
        ...github,
        rest: {
          ...github.rest,
          pulls: { ...github.rest.pulls, get: async () => ({ data: { ...pr, head: { ...pr.head, sha: 'new' } } }) },
        },
      },
    }),
    /head changed/,
  );
  await assert.rejects(run({ ...options, github: { ...github, paginate: async () => [] } }), /Incomplete PR file list/);
});
test('paths match root Go files, exclusions and branch selection', () => {
  const workflow = { paths: ['**/*.go', '!cli/**'], branches: ['main'] };
  assert.equal(workflowApplies(workflow, ['main.go'], 'main'), true);
  assert.equal(workflowApplies(workflow, ['cli/main.go'], 'main'), false);
  assert.equal(workflowApplies(workflow, ['internal/server.go'], 'master'), false);
  assert.equal(workflowApplies({ pathsIgnore: ['docs/**'] }, ['docs/a.md'], 'main'), false);
  assert.throws(() => workflowApplies({ paths: ['src/[ab].js'] }, ['src/a.js'], 'main'), /Unsupported/);
});
test('only the actual Dependabot bot may use generated release notes', () => {
  const pr = {
    title: 'build(deps): Bump dependency',
    body: 'Release notes and dependency comparison.',
    user: { login: 'dependabot[bot]', type: 'Bot' },
  };
  assert.deepEqual(validatePr(pr), []);
  assert.ok(validatePr({ ...pr, user: { login: 'a-person', type: 'User' } }).length);
});
test('missing, cancelled and newest failed CI never pass; other heads cannot satisfy the gate', () => {
  const expected = [{ name: 'CI', path: '.github/workflows/ci.yml' }];
  const success = {
    id: 1,
    run_attempt: 1,
    path: expected[0].path,
    event: 'pull_request',
    head_sha: 'abc',
    status: 'completed',
    conclusion: 'success',
  };
  assert.deepEqual(evaluateRuns(expected, [success], 'abc'), { waiting: [], failed: [] });
  assert.equal(evaluateRuns(expected, [success], 'other').waiting.length, 1);
  assert.equal(evaluateRuns(expected, [success, { ...success, id: 2, conclusion: 'failure' }], 'abc').failed.length, 1);
  assert.equal(evaluateRuns(expected, [{ ...success, conclusion: 'cancelled' }], 'abc').failed.length, 1);
});

function fixture() {
  const pr = {
    number: 17, state: 'open', title: 'ci: improve gate', body, changed_files: 1,
    head: { sha: 'a'.repeat(40), ref: 'change', repo: { id: 10 } }, base: { ref: 'main' },
  };
  const config = { workflows: [
    { name: 'CI', path: '.github/workflows/ci.yml' },
    { name: 'CodeQL', path: '.github/workflows/codeql.yml' },
    { name: 'DevOps gate tests', path: '.github/workflows/devops-gate-tests.yml' },
  ] };
  const state = { pr, files: [{ filename: 'main.go' }], checks: [], writes: [], reads: 0, snapshots: 0 };
  state.runs = config.workflows.map((workflow, index) => ({
    id: index + 1, run_attempt: 1, event: 'pull_request', path: workflow.path,
    head_sha: pr.head.sha, head_branch: pr.head.ref, head_repository: { id: pr.head.repo.id },
    pull_requests: [{ number: pr.number, head: { sha: pr.head.sha }, base: { ref: 'main' } }],
    status: 'completed', conclusion: 'success',
  }));
  state.eventRun = state.runs[0];
  const github = {
    rest: {
      pulls: {
        listFiles: 'files', list: 'prs',
        get: async () => { state.reads++; return { data: structuredClone(state.pr) }; },
      },
      actions: { listWorkflowRunsForRepo: 'runs', getWorkflowRun: async () => ({ data: state.eventRun }) },
      checks: {
        listForRef: 'checks',
        create: async request => {
          state.writes.push({ action: 'create', ...request });
          const check = { ...request, id: 100, app: { slug: 'github-actions' } };
          state.checks.push(check);
          return { data: check };
        },
        update: async request => {
          state.writes.push({ action: 'update', ...request });
          const check = state.checks.find(check => check.id === request.check_run_id);
          Object.assign(check, request);
          return { data: check };
        },
      },
    },
    paginate: async endpoint => {
      if (endpoint === 'files') return state.files;
      if (endpoint === 'prs') return [state.pr];
      if (endpoint === 'checks') return state.checks;
      assert.equal(endpoint, 'runs');
      state.snapshots++;
      return state.runs;
    },
  };
  const context = {
    eventName: 'pull_request_target', repo: { owner: 'example', repo: 'example' },
    serverUrl: 'https://github.com', ref: 'refs/heads/main', runId: 99, payload: { pull_request: structuredClone(pr) },
  };
  return { state, github, context, config, core: { info() {} } };
}

test('queued CI returns pending after one snapshot without polling, timeout or success', async () => {
  const f = fixture();
  f.state.runs[1].status = 'queued';
  f.state.runs[1].conclusion = null;
  await refresh(f);
  assert.equal(f.state.snapshots, 1);
  assert.equal(f.state.writes[0].name, 'ci-gate');
  assert.equal(f.state.writes[0].head_sha, f.state.pr.head.sha);
  const result = f.state.writes.at(-1);
  assert.equal(result.status, 'queued');
  assert.equal(result.conclusion, undefined);
  assert.match(result.output.summary, /CodeQL/);
});

test('upstream completion event refreshes the current PR and releases the check', async () => {
  const f = fixture();
  f.context.eventName = 'workflow_run';
  f.context.payload = { workflow_run: { id: 1, conclusion: 'failure', pull_requests: [{ number: 999 }] } };
  await refresh(f);
  assert.equal(f.state.writes.at(-1).conclusion, 'success');
  assert.equal(f.state.snapshots, 1);
  assert.equal(f.state.writes[0].external_id, `devops-gate:17:${f.state.pr.head.sha}`);
});

test('metadata errors, failed/cancelled/skipped CI and incomplete files block the required check', async () => {
  const changes = [
    f => { f.state.pr.body = 'missing template'; },
    f => { f.state.pr.title = 'not conventional'; },
    f => { f.state.files = []; },
    ...['failure', 'cancelled', 'skipped', 'neutral', 'timed_out'].map(conclusion =>
      f => { f.state.runs[0].conclusion = conclusion; }),
  ];
  for (const change of changes) {
    const f = fixture(); change(f);
    await refresh(f);
    assert.equal(f.state.writes.at(-1).conclusion, 'failure');
  }
});

test('newest run and newest attempt override historical success or failure', async () => {
  const f = fixture();
  const success = structuredClone(f.state.runs[0]);
  f.state.runs.push({ ...success, id: 50, status: 'in_progress', conclusion: null });
  await refresh(f);
  assert.equal(f.state.writes.at(-1).status, 'queued');
  f.state.runs.at(-1).status = 'completed';
  f.state.runs.at(-1).conclusion = 'failure';
  await refresh(f);
  assert.equal(f.state.writes.at(-1).conclusion, 'failure');
  f.state.runs.push({ ...success, id: 50, run_attempt: 2 });
  await refresh(f);
  assert.equal(f.state.writes.at(-1).conclusion, 'success');
});

test('foreign PR, branch, repository, base, SHA and non-PR runs cannot satisfy the gate', async () => {
  for (const changed of [
    { head_sha: 'b'.repeat(40) }, { event: 'push' }, { event: 'workflow_dispatch' },
    { head_branch: 'other' }, { head_repository: { id: 999 } }, { pull_requests: [] },
    { pull_requests: [{ number: 999, head: { sha: 'a'.repeat(40) }, base: { ref: 'main' } }] },
    { pull_requests: [{ number: 17, head: { sha: 'a'.repeat(40) }, base: { ref: 'other' } }] },
    { pull_requests: [{ number: 17, head: { sha: 'b'.repeat(40) }, base: { ref: 'main' } }] },
  ]) {
    const f = fixture(); Object.assign(f.state.runs[0], changed);
    await refresh(f);
    assert.equal(f.state.writes.at(-1).status, 'queued');
    assert.equal(f.state.writes.at(-1).conclusion, undefined);
  }
});

test('old-head, closed-PR, wrong-path and unrelated workflow events do not publish checks', async () => {
  for (const change of [
    f => { f.state.eventRun = { ...f.state.eventRun, head_sha: 'old' }; },
    f => { f.state.pr.state = 'closed'; },
    f => { f.state.eventRun = { ...f.state.eventRun, path: '.github/workflows/unrelated.yml' }; },
    f => { f.state.eventRun = { ...f.state.eventRun, event: 'push' }; },
    f => { f.state.eventRun = { ...f.state.eventRun, head_repository: { id: 999 } }; },
    f => { f.state.eventRun = { ...f.state.eventRun, pull_requests: [{ number: 17, head: { sha: 'old' }, base: { ref: 'main' } }] }; },
  ]) {
    const f = fixture(); change(f);
    f.context.eventName = 'workflow_run'; f.context.payload = { workflow_run: { id: 1 } };
    await refresh(f);
    assert.deepEqual(f.state.writes, []);
  }
});

test('head, body and base changes during aggregation invalidate the snapshot', async () => {
  for (const change of [
    pr => { pr.head.sha = 'b'.repeat(40); }, pr => { pr.body = 'edited'; },
    pr => { pr.base.ref = 'other'; }, pr => { pr.state = 'closed'; },
  ]) {
    const f = fixture();
    f.github.rest.pulls.get = async () => {
      if (++f.state.reads === 3) change(f.state.pr);
      return { data: structuredClone(f.state.pr) };
    };
    await refresh(f);
    assert.equal(f.state.writes.at(-1).conclusion, 'cancelled');
    assert.equal(f.state.writes[0].head_sha, 'a'.repeat(40));
  }
});

test('renamed old paths remain in workflow applicability and cannot hide a required check', async () => {
  const f = fixture();
  f.config.workflows[1].paths = ['sensitive/**'];
  f.state.files = [{ filename: 'docs/moved.md', previous_filename: 'sensitive/token.go' }];
  f.state.runs = f.state.runs.filter(run => run.path !== f.config.workflows[1].path);
  await refresh(f);
  assert.equal(f.state.writes.at(-1).status, 'queued');
  assert.match(f.state.writes.at(-1).output.summary, /CodeQL/);
});

test('fork events without PR hints are resolved read-only but missing association never passes', async () => {
  const f = fixture();
  f.state.eventRun = { ...f.state.eventRun, pull_requests: [] };
  f.state.runs[0] = f.state.eventRun;
  f.context.eventName = 'workflow_run'; f.context.payload = { workflow_run: { id: 1 } };
  await refresh(f);
  assert.equal(f.state.writes[0].name, 'ci-gate');
  assert.equal(f.state.writes.at(-1).status, 'queued');
});

test('manual recovery only accepts a numeric PR ID and validates fresh API metadata', async () => {
  const f = fixture(); f.context.eventName = 'workflow_dispatch';
  for (const value of ['', '-1', '1; command', '1.2', '9007199254740992']) {
    f.context.payload = { inputs: { pull_request: value }, repository: { default_branch: 'main' } };
    await assert.rejects(eventPullNumbers(f), /valid pull request/);
  }
  f.context.payload = { inputs: { pull_request: '17' }, repository: { default_branch: 'main' } };
  await refresh(f);
  assert.equal(f.state.writes.at(-1).conclusion, 'success');
  f.context.ref = 'refs/heads/change';
  await assert.rejects(eventPullNumbers(f), /default branch/);
});

test('API errors cannot publish success and failed result writes leave a blocking queued check', async () => {
  const f = fixture();
  const paginate = f.github.paginate;
  f.github.paginate = async endpoint => {
    if (endpoint === 'checks') return paginate(endpoint);
    throw new Error('API unavailable');
  };
  await refresh(f);
  assert.equal(f.state.writes.at(-1).conclusion, 'failure');
  const broken = fixture();
  broken.github.rest.checks.update = async () => { throw new Error('write unavailable'); };
  await assert.rejects(refresh(broken), /write unavailable/);
  assert.equal(broken.state.writes.length, 1);
  assert.equal(broken.state.writes[0].status, 'queued');
});

test('refresh reuses only publisher-owned checks and resets success to pending during reruns', async () => {
  const f = fixture();
  await refresh(f);
  assert.equal(f.state.writes.at(-1).conclusion, 'success');
  f.state.runs[0].status = 'in_progress';
  f.state.runs[0].conclusion = null;
  await refresh(f);
  assert.equal(f.state.writes.filter(write => write.action === 'create').length, 1);
  const reset = f.state.writes.at(-2);
  assert.equal(reset.check_run_id, 100);
  assert.equal(reset.status, 'queued');
  assert.equal(reset.conclusion, undefined, 'sending conclusion would auto-complete a queued check');
  assert.equal(f.state.writes.at(-1).status, 'queued');
  assert.equal(f.state.writes.at(-1).conclusion, undefined);
  const foreign = fixture();
  foreign.state.checks = [
    { id: 90, name: 'ci-gate', external_id: 'unrelated', app: { slug: 'github-actions' } },
    { id: 91, name: 'ci-gate', external_id: `devops-gate:17:${foreign.state.pr.head.sha}`, app: { slug: 'other-app' } },
  ];
  await refresh(foreign);
  assert.ok(foreign.state.writes.every(write => ![90, 91].includes(write.check_run_id)));
});

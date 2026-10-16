'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { execFileSync } = require('node:child_process');
const { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync } = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { selectE2e, readPlan, verifyResults, planFromGit } = require('./e2e-plan.cjs');
const { runSelected } = require('./run-selected-e2e.cjs');
const policy = require('./devops-ci.json').e2e;
const scripts = require('../package.json').scripts;
const full = selectE2e('push');
const docs = selectE2e('pull_request', ['docs/architecture.md']);
const pr = files => selectE2e('pull_request', files);

function assertFull(plan) {
  assert.deepEqual(plan.compose, full.compose);
  assert.equal(plan.kubernetes, true);
  assert.equal(plan.backup, true);
}

test('main pushes and manual runs retain exactly the default Compose chain plus Kind and backup', () => {
  const defaultScenarios = [...scripts['test:e2e'].matchAll(/npm run test:e2e:([\w-]+)/g)].map(match => match[1]);
  assert.deepEqual(full.compose, defaultScenarios);
  for (const event of ['push', 'workflow_dispatch', 'merge_group', undefined]) assertFull(selectE2e(event, ['README.md']));
  assert.ok(!full.compose.includes('offline-license'));
  for (const scope of policy.scopes) {
    assert.ok(scope.paths.length > 0);
    assert.ok(scope.compose.every(name => full.compose.includes(name)), scope.name);
  }
  for (const name of [...full.compose, 'm3-kubernetes', 'backup-restore']) assert.ok(scripts['test:e2e:' + name]);
});

test('documentation-only skips E2E; documentation cannot hide code, scripts or CI edits', () => {
  assert.deepEqual(docs.compose, []);
  assert.equal(docs.kubernetes, false);
  assert.equal(docs.backup, false);
  assert.deepEqual(pr(['README.md', '.github/pull_request_template.md', 'docs/nested/runbook.md']).compose, []);
  for (const file of ['docs/run-test.mjs', '.github/workflows/m0.yml', 'new-component/main.go']) assertFull(pr(['README.md', file]));
});

test('isolated scenario edits rerun their scenario, including deletion paths', () => {
  for (const name of full.compose) {
    const file = 'tests/e2e/' + (name === 'upgrade' ? 'control-upgrade' : name) + '.mjs';
    const plan = pr([file]);
    assert.deepEqual(plan.compose, [name]);
    assert.equal(plan.kubernetes, false);
    assert.equal(plan.backup, false);
  }
  assert.deepEqual(pr(['tests/e2e/m3-kubernetes.mjs']), { compose: [], kubernetes: true, backup: false, reasons: ['Kubernetes scenario'] });
  assert.deepEqual(pr(['tests/e2e/backup-restore.mjs']), { compose: [], kubernetes: false, backup: true, reasons: ['backup scenario'] });
});

test('runtime dependencies select the affected cross-component chains', () => {
  const gateway = pr(['services/gateway-authorizer/internal/proxy/handler.go']);
  assert.deepEqual(gateway.compose, ['m1-control', 'm1-gateway', 'gateway-native', 'm1-client', 'm3-data-plane']);
  assert.equal(gateway.kubernetes, true);
  assert.equal(gateway.backup, false);
  assert.deepEqual(pr(['services/gateway-reconciler/internal/controller/controller.go']).compose, ['gateway-native', 'm3-data-plane']);
  assert.deepEqual(pr(['examples/node-agent/src/index.ts']).compose, ['m0', 'm1-client', 'm2-agent']);
  const model = pr(['services/control-service/internal/httpapi/models.go']);
  assert.ok(model.compose.includes('m2-control'), 'model validation is also covered by credential control E2E');
  const credential = pr(['services/control-service/internal/credential/service.go']);
  assert.ok(credential.compose.includes('m2-agent'));
  assert.equal(credential.kubernetes, true);
  const skill = pr(['services/control-service/internal/blob/store.go']);
  assert.deepEqual(skill.compose, ['m0', 'm2-control', 'm2-agent', 'runtime', 'upgrade']);
  assert.equal(skill.backup, true);
  const events = pr(['services/control-service/internal/httpapi/telemetry.go']);
  assert.ok(events.compose.includes('m1-client'), 'client model telemetry must remain covered');
  assert.ok(events.compose.includes('m2-control'), 'credential audit checks must remain covered');
  assert.ok(events.compose.includes('runtime'));
  const backup = pr(['scripts/restore-compose.mjs']);
  assert.deepEqual(backup.compose, []);
  assert.equal(backup.backup, true);
});

test('multiple scopes use a stable union with no duplicates', () => {
  const plan = pr(['services/control-service/internal/blob/store.go', 'services/gateway-reconciler/controller.go', 'tests/e2e/m1-control.mjs']);
  assert.deepEqual(plan.compose, ['m0', 'm1-control', 'gateway-native', 'm2-control', 'm2-agent', 'runtime', 'm3-data-plane', 'upgrade']);
  assert.equal(plan.kubernetes, true);
  assert.equal(plan.backup, true);
  assert.deepEqual(pr(['services/gateway-reconciler/controller.go', 'services/control-service/internal/blob/store.go', 'tests/e2e/m1-control.mjs']).compose, plan.compose);
});

test('security, shared state, dependency and unknown changes fail back to all E2E', () => {
  for (const file of [
    'services/control-service/internal/auth/tokens.go', 'services/control-service/internal/db/migrations/0040.sql',
    'services/control-service/internal/httpapi/server.go', 'services/control-service/internal/httpapi/auth.go',
    'services/control-service/internal/httpapi/activation.go', 'services/control-service/internal/app/app.go',
    'services/control-service/internal/repository/users.go', 'services/control-service/internal/config/config.go',
    'services/internal/runtime/health.go', 'packages/aep-sdk-node/src/client.ts', 'openapi/aep-v1.openapi.yaml',
    'go.mod', 'go.sum', 'package.json', 'package-lock.json', 'deploy/compose/compose.yaml',
    '.github/devops-ci.json', '.github/e2e-plan.cjs', 'tests/e2e/shared-helper.mjs', 'new-module/main.go',
  ]) assertFull(pr([file]));
  for (const files of [undefined, null, [null], [''], ['docs\\file.md']]) assertFull(pr(files));
});

test('unavailable or truncated Git inventories select full coverage', () => {
  const env = { GITHUB_EVENT_NAME: 'pull_request', AEP_CI_BASE_SHA: 'a'.repeat(40), AEP_CI_HEAD_SHA: 'b'.repeat(40) };
  assertFull(planFromGit({ ...env, AEP_CI_HEAD_SHA: 'HEAD; command' }, () => { throw new Error('must not call'); }));
  assertFull(planFromGit(env, () => { throw new Error('missing history'); }));
  assertFull(planFromGit(env, () => 'README.md'));
  assertFull(planFromGit(env, () => Buffer.from('README.md\0')));
  assert.deepEqual(planFromGit(env, () => 'README.md\0').compose, []);
});

test('real Git three-dot diff handles advanced main, removed files, renames and unusual filenames', () => {
  const cwd = mkdtempSync(path.join(os.tmpdir(), 'aep-e2e-plan-'));
  const git = (...args) => execFileSync('git', args, { cwd, encoding: 'utf8' }).trim();
  const put = (file, data) => {
    mkdirSync(path.dirname(path.join(cwd, file)), { recursive: true });
    writeFileSync(path.join(cwd, file), data);
  };
  try {
    git('init', '-b', 'main');
    git('config', 'user.email', 'e2e-plan@example.invalid');
    git('config', 'user.name', 'E2E selection test');
    put('tests/e2e/m1-control.mjs', 'fixture');
    put('docs/guide.md', 'old');
    git('add', '.'); git('commit', '-m', 'base');
    git('checkout', '-b', 'change');
    put('docs/guide.md', 'new'); git('add', '.'); git('commit', '-m', 'docs');
    const docHead = git('rev-parse', 'HEAD');
    git('checkout', 'main');
    put('services/control-service/internal/auth/new.go', 'shared change on main');
    git('add', '.'); git('commit', '-m', 'advance main');
    const base = git('rev-parse', 'HEAD');
    const evaluate = head => planFromGit({ GITHUB_EVENT_NAME: 'pull_request', AEP_CI_BASE_SHA: base, AEP_CI_HEAD_SHA: head },
      (baseSha, headSha) => execFileSync('git', ['diff', '--name-only', '-z', '--no-renames', baseSha + '...' + headSha], { cwd, encoding: 'utf8' }));
    assert.deepEqual(evaluate(docHead).compose, [], 'unrelated advanced-main changes are excluded');
    git('checkout', 'change');
    git('rm', 'tests/e2e/m1-control.mjs'); git('commit', '-m', 'remove scenario');
    assert.deepEqual(evaluate(git('rev-parse', 'HEAD')).compose, ['m1-control']);
    git('mv', 'docs/guide.md', 'docs/$(untrusted command).mjs'); git('commit', '-m', 'rename to unknown executable');
    assertFull(evaluate(git('rev-parse', 'HEAD')));
  } finally { rmSync(cwd, { recursive: true, force: true }); }
});

function needsFor(plan, event = 'pull_request') {
  return Object.fromEntries([
    ['e2e-plan', { result: 'success', outputs: { e2e_plan: JSON.stringify(plan) } }],
    ['sdk-gate', { result: 'success' }],
    ...['control-service', 'example-agent'].map(name => [name, { result: 'success' }]),
    ['coverage-ratchet', { result: event === 'pull_request' ? 'success' : 'skipped' }],
    ['compose-e2e', { result: plan.compose.length ? 'success' : 'skipped' }],
    ['data-plane-kubernetes', { result: plan.kubernetes ? 'success' : 'skipped' }],
    ['backup-restore', { result: plan.backup ? 'success' : 'skipped' }],
  ]);
}

test('aggregation accepts only successful prerequisites and precisely planned skips', () => {
  for (const plan of [docs, full, pr(['examples/node-agent/src/index.ts']), pr(['scripts/backup-compose.mjs'])])
    assert.doesNotThrow(() => verifyResults(needsFor(plan), 'pull_request'));
  for (const event of ['push', 'workflow_dispatch', 'merge_group']) {
    assert.doesNotThrow(() => verifyResults(needsFor(full, event), event));
    assert.throws(() => verifyResults(needsFor(docs, event), event), /complete E2E/);
  }
  for (const name of Object.keys(needsFor(full))) {
    for (const result of ['failure', 'cancelled', 'skipped', undefined]) {
      const needs = needsFor(full);
      needs[name].result = result;
      assert.throws(() => verifyResults(needs, 'pull_request'), /did not|expected/);
    }
  }
  for (const result of ['success', 'failure', 'cancelled']) {
    const needs = needsFor(docs);
    needs['compose-e2e'].result = result;
    assert.throws(() => verifyResults(needs, 'pull_request'), /expected skipped/);
  }
  const missing = needsFor(full);
  delete missing['e2e-plan'].outputs;
  assert.throws(() => verifyResults(missing, 'pull_request'));
});

test('executor runs only validated scenarios, stops on failure and never shells out', () => {
  const plan = JSON.stringify(pr(['tests/e2e/m1-control.mjs', 'tests/e2e/m2-agent.mjs']));
  const calls = [];
  const run = (command, args, options) => { calls.push({ command, args, options }); return { status: 0 }; };
  assert.equal(runSelected(plan, run, ['npm']), 0);
  assert.deepEqual(calls.map(call => call.args), [['run', 'test:e2e:m1-control'], ['run', 'test:e2e:m2-agent']]);
  assert.ok(calls.every(call => call.options.shell === false));
  let attempts = 0;
  assert.equal(runSelected(plan, () => { attempts++; return { status: 7 }; }, ['npm']), 7);
  assert.equal(attempts, 1);
  assert.equal(runSelected(plan, () => ({ status: null, signal: 'SIGTERM' }), ['npm']), 1);
  assert.throws(() => runSelected(plan, () => ({ error: new Error('spawn failed') }), ['npm']), /spawn failed/);
  for (const value of [undefined, '{}', '{', JSON.stringify({ ...full, compose: ['m0', 'm0'] }), JSON.stringify({ ...full, compose: ['m0; echo unsafe'] })]) {
    assert.throws(() => readPlan(value));
    assert.throws(() => runSelected(value, () => assert.fail('invalid plan must not execute'), ['npm']));
  }
  assert.throws(() => runSelected(JSON.stringify(docs), run, ['npm']), /without selected/);
});

test('workflow graph keeps mandatory checks, consumes the plan and always validates all results', () => {
  // Same lockfile-provided YAML parser as the native gateway E2E fixtures.
  const workflow = require('js-yaml').load(readFileSync(path.join(__dirname, 'workflows/m0.yml'), 'utf8'));
  assert.ok(Object.hasOwn(workflow.on, 'pull_request'));
  assert.ok(Object.hasOwn(workflow.on, 'workflow_dispatch'));
  assert.ok(workflow.on.push.branches.includes('main'));
  const jobs = workflow.jobs;
  for (const name of ['e2e-plan', 'sdk-gate', 'control-service', 'example-agent']) assert.equal(jobs[name].if, undefined);
  assert.equal(jobs['coverage-ratchet'].if, "github.event_name == 'pull_request' && needs.control-service.result == 'success'");
  const planner = jobs['e2e-plan'];
  assert.equal(planner.steps.find(step => step.uses === 'actions/checkout@v4').with['fetch-depth'], 0);
  for (const output of ['e2e_plan', 'compose', 'kubernetes', 'backup'])
    assert.equal(planner.outputs[output], '${{ steps.e2e-plan.outputs.' + output + ' }}');
  assert.ok(planner.steps.some(step => step.run === 'node --test .github/e2e-plan.test.cjs'));
  assert.equal(jobs['control-service'].needs, undefined);
  assert.equal(jobs['example-agent'].needs, undefined);
  for (const [name, output] of [['compose-e2e', 'compose'], ['data-plane-kubernetes', 'kubernetes'], ['backup-restore', 'backup']]) {
    assert.deepEqual(jobs[name].needs, ['e2e-plan', 'sdk-gate', 'control-service', 'example-agent']);
    assert.equal(jobs[name].if, "${{ needs.e2e-plan.outputs." + output + " == 'true' }}");
  }
  const executor = jobs['compose-e2e'].steps.find(step => step.run === 'node .github/run-selected-e2e.cjs');
  assert.equal(executor.env.AEP_E2E_PLAN, '${{ needs.e2e-plan.outputs.e2e_plan }}');
  assert.equal(workflow.concurrency['cancel-in-progress'], true);
  const gate = jobs['release-gate'];
  assert.equal(gate.if, '${{ always() }}');
  assert.deepEqual(new Set(gate.needs), new Set(Object.keys(needsFor(full))));
  assert.equal(gate.steps.find(step => step.run === 'node .github/e2e-status.cjs').env.AEP_CI_NEEDS, '${{ toJSON(needs) }}');
});

'use strict';
const { execFileSync } = require('node:child_process');
const { appendFileSync } = require('node:fs');
const { globMatches } = require('./devops-gate.cjs');
const inventory = require('./devops-ci.json').e2e;

function fullPlan(reason) {
  return { compose: [...inventory.compose], kubernetes: true, backup: true, reasons: [reason] };
}

// Unknown paths deliberately select every E2E. Narrow rules must be reviewed
// alongside the dependencies of the scenario, not merely its filename.
function selectE2e(event, files) {
  if (event !== 'pull_request') return fullPlan('main/push/manual verification');
  if (!Array.isArray(files) || !files.every(file => typeof file === 'string' && file && !file.includes('\\')))
    return fullPlan('incomplete changed-file inventory');
  const compose = new Set();
  const reasons = new Set();
  let kubernetes = false;
  let backup = false;
  for (const file of files) {
    if (inventory.documentationPaths.some(pattern => globMatches(file, pattern))) continue;
    if (inventory.fullPaths.some(pattern => globMatches(file, pattern))) return fullPlan('shared dependency: ' + file);
    const scenario = inventory.compose.find(name => file === 'tests/e2e/' + (name === 'upgrade' ? 'control-upgrade' : name) + '.mjs');
    if (scenario) {
      compose.add(scenario);
      reasons.add('scenario: ' + scenario);
      continue;
    }
    if (file === 'tests/e2e/m3-kubernetes.mjs') {
      kubernetes = true;
      reasons.add('Kubernetes scenario');
      continue;
    }
    if (file === 'tests/e2e/backup-restore.mjs') {
      backup = true;
      reasons.add('backup scenario');
      continue;
    }
    const scopes = inventory.scopes.filter(scope => scope.paths.some(pattern => globMatches(file, pattern)));
    if (!scopes.length) return fullPlan('unmapped path: ' + file);
    for (const scope of scopes) {
      for (const name of scope.compose) compose.add(name);
      kubernetes ||= scope.kubernetes === true;
      backup ||= scope.backup === true;
      reasons.add(scope.name);
    }
  }
  return { compose: inventory.compose.filter(name => compose.has(name)), kubernetes, backup, reasons: [...reasons] };
}

function readPlan(value) {
  const plan = JSON.parse(value);
  if (!plan || !Array.isArray(plan.compose) || typeof plan.kubernetes !== 'boolean' || typeof plan.backup !== 'boolean' ||
      !plan.compose.every(name => inventory.compose.includes(name)) || new Set(plan.compose).size !== plan.compose.length)
    throw new Error('Invalid E2E plan; refusing to skip required checks.');
  return plan;
}

function verifyResults(needs, event) {
  for (const name of ['e2e-plan', 'sdk-gate', 'control-service', 'example-agent']) {
    if (needs[name]?.result !== 'success') throw new Error(name + ' did not succeed');
  }
  const coverage = event === 'pull_request' ? 'success' : 'skipped';
  if (needs['coverage-ratchet']?.result !== coverage) throw new Error('coverage-ratchet did not complete as required');
  const plan = readPlan(needs['e2e-plan']?.outputs?.e2e_plan ?? needs['sdk-gate']?.outputs?.e2e_plan);
  if (event !== 'pull_request' && (!plan.kubernetes || !plan.backup ||
      plan.compose.length !== inventory.compose.length))
    throw new Error('Push/manual verification requires the complete E2E plan');
  for (const [name, selected] of [
    ['compose-e2e', plan.compose.length > 0],
    ['data-plane-kubernetes', plan.kubernetes],
    ['backup-restore', plan.backup],
  ]) {
    const required = selected ? 'success' : 'skipped';
    if (needs[name]?.result !== required) throw new Error(name + ': expected ' + required + ', got ' + needs[name]?.result);
  }
}

function planFromGit(env, diff = (base, head) => execFileSync('git', ['diff', '--name-only', '-z', '--no-renames', base + '...' + head], { encoding: 'utf8', maxBuffer: 16 * 1024 * 1024 })) {
  if (env.GITHUB_EVENT_NAME !== 'pull_request') return selectE2e(env.GITHUB_EVENT_NAME);
  // Both refs arrive as environment variables, never interpolated into shell code.
  if (![env.AEP_CI_BASE_SHA, env.AEP_CI_HEAD_SHA].every(sha => /^[0-9a-f]{40}$/.test(sha ?? '')))
    return fullPlan('missing PR commits');
  try {
    const output = diff(env.AEP_CI_BASE_SHA, env.AEP_CI_HEAD_SHA);
    if (output && !output.endsWith('\0')) return fullPlan('truncated changed-file inventory');
    return selectE2e('pull_request', output ? output.slice(0, -1).split('\0') : []);
  } catch {
    return fullPlan('changed-file comparison unavailable');
  }
}

if (require.main === module) {
  const plan = planFromGit(process.env);
  console.log(JSON.stringify(plan));
  if (process.env.GITHUB_OUTPUT) appendFileSync(process.env.GITHUB_OUTPUT,
    'e2e_plan=' + JSON.stringify(plan) + '\n' +
    'compose=' + (plan.compose.length > 0) + '\n' +
    'kubernetes=' + plan.kubernetes + '\n' +
    'backup=' + plan.backup + '\n');
  if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY,
    '### Selected E2E checks\n\n' +
    '- Compose: ' + (plan.compose.join(', ') || 'none') + '\n' +
    '- Kubernetes: ' + plan.kubernetes + '\n' +
    '- Backup/restore: ' + plan.backup + '\n');
}

module.exports = { selectE2e, readPlan, verifyResults, planFromGit };

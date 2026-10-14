'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { verifyNeeds } = require('./ci-results.cjs');
const config = require('./devops-ci.json');

test('all callers must succeed; skipped and cancelled cannot pass', () => {
  const required = ['checks', 'security', 'codeql'];
  const success = Object.fromEntries(required.map((name) => [name, { result: 'success' }]));
  assert.doesNotThrow(() => verifyNeeds(success, required));
  for (const result of ['failure', 'cancelled', 'skipped', 'pending', undefined]) {
    for (const name of required)
      assert.throws(() => verifyNeeds({ ...success, [name]: { result } }, required), /Required CI/);
  }
  assert.throws(() => verifyNeeds({ checks: { result: 'success' } }, required), /inventory/);
  assert.throws(() => verifyNeeds({ ...success, unknown: { result: 'success' } }, required), /inventory/);
});

test('the configured final gate rejects missing or unsuccessful dependency audits', () => {
  const required = config.orchestrator.needs;
  assert.ok(required.includes('dependency-audit'));
  const success = Object.fromEntries(required.map((name) => [name, { result: 'success' }]));
  assert.doesNotThrow(() => verifyNeeds(success, required));
  for (const result of ['failure', 'cancelled', 'skipped', undefined]) {
    assert.throws(() => verifyNeeds({ ...success, 'dependency-audit': { result } }, required), /Required CI/);
  }
  delete success['dependency-audit'];
  assert.throws(() => verifyNeeds(success, required), /inventory/);
});

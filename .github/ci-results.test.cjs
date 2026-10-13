'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { verifyNeeds } = require('./ci-results.cjs');

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

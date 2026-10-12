'use strict';
const { verifyResults } = require('./e2e-plan.cjs');
verifyResults(JSON.parse(process.env.AEP_CI_NEEDS), process.env.GITHUB_EVENT_NAME);
console.log('All required checks and selected E2E scenarios passed.');

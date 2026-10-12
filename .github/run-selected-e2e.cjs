'use strict';
const { spawnSync } = require('node:child_process');
const { existsSync } = require('node:fs');
const path = require('node:path');
const { readPlan } = require('./e2e-plan.cjs');

function npmCommand() {
  // Launch npm through Node on Windows, without invoking cmd.exe or a shell.
  if (process.platform !== 'win32') return ['npm'];
  const cli = path.join(path.dirname(process.execPath), 'node_modules/npm/bin/npm-cli.js');
  if (!existsSync(cli)) throw new Error('Cannot locate npm CLI beside Node.js');
  return [process.execPath, cli];
}

function runSelected(value, run = spawnSync, command = npmCommand()) {
  const plan = readPlan(value);
  if (!plan.compose.length) throw new Error('Compose job ran without selected scenarios.');
  for (const scenario of plan.compose) {
    console.log('Running E2E: ' + scenario);
    const child = run(command[0], [...command.slice(1), 'run', 'test:e2e:' + scenario], { stdio: 'inherit', shell: false });
    if (child.error) throw child.error;
    if (child.status !== 0) return child.status ?? 1;
  }
  return 0;
}

if (require.main === module) process.exitCode = runSelected(process.env.AEP_E2E_PLAN);
module.exports = { runSelected };

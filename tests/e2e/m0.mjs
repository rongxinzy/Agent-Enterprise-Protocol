import {spawn} from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

import yazl from 'yazl';

import {AepClient, MemoryTokenStore} from '../../packages/aep-sdk-node/dist/index.js';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const composeFile = path.join(root, 'deploy', 'compose', 'compose.yaml');
const project = 'aep-m0-e2e';
const port = process.env.AEP_E2E_PORT ?? '18080';
const baseUrl = `http://localhost:${port}`;
const composeEnv = {AEP_PORT: port, AEP_MINIO_CONSOLE_PORT: process.env.AEP_E2E_MINIO_CONSOLE_PORT ?? '19001'};
const runId = Date.now().toString(36);
const tempDirectory = fs.mkdtempSync(path.join(os.tmpdir(), 'aep-m0-e2e-'));

try {
  await command('docker', ['compose', '-p', project, '-f', composeFile, 'up', '-d', '--build'], composeEnv);
  await waitForHealth();
  await runScenario();
  console.log('AEP M0 end-to-end scenario passed.');
} finally {
  await command('docker', ['compose', '-p', project, '-f', composeFile, 'down', '-v', '--remove-orphans'], composeEnv, true);
  fs.rmSync(tempDirectory, {recursive: true, force: true});
}

async function runScenario() {
  const admin = new AepClient({baseUrl, agentId: `e2e-admin-${runId}`, agentVersion: 'e2e', platform: platform(), tokenStore: new MemoryTokenStore()});
  await admin.loginWithPassword({enterpriseId: 'demo', username: 'admin', password: 'change-this-admin-password'});

  const username = `user-${runId}`;
  const password = 'temporary-password-123';
  const user = await runCli(['user', 'create', '--user', username, '--display-name', `E2E User ${runId}`, '--temporary-password', password, '--require-password-change=false']);
  const skillId = `review-${runId}`;
  const archivePath = path.join(tempDirectory, `${skillId}.zip`);
  fs.writeFileSync(archivePath, await createSkillArchive());
  await runCli(['skill', 'create', '--skill-id', skillId, '--name', `Review ${runId}`, '--description', 'M0 end-to-end Skill']);
  await runCli(['skill', 'upload', '--skill-id', skillId, '--version', '1.0.0', '--file', archivePath]);
  await runCli(['skill', 'publish', '--skill-id', skillId, '--version', '1.0.0']);
  const assignment = await runCli(['skill', 'assign', '--skill-id', skillId, '--subject-type', 'user', '--subject-id', String(user.id)]);

  const agentId = `e2e-agent-${runId}`;
  const agentData = path.join(tempDirectory, 'agent');
  await runAgent(agentId, username, password, agentData);
  const installEvent = await admin.createControlEvent({
    type: 'skill.manifest.changed', scope: {type: 'agent', id: agentId},
    resource: {type: 'skill', id: skillId, revision: '1'}, task: {type: 'skill.reconcile'},
    expiresAt: new Date(Date.now() + 60_000).toISOString(), supersedesKey: `skill:${skillId}:${agentId}`,
  });
  const installedSkill = path.join(agentData, 'managed-skills', skillId, 'SKILL.md');
  await runCompose('stop', 'minio');
  await runAgent(agentId, username, password, agentData);
  assert(!fs.existsSync(installedSkill), 'Skill was installed while MinIO was unavailable');
  await assertDelivery(admin, String(installEvent.eventId), 'failed');

  await runCompose('start', 'minio');
  await waitForSkillInstall(agentId, username, password, agentData, installedSkill);
  const recoveredDelivery = await assertDelivery(admin, String(installEvent.eventId), 'succeeded');
  assert(recoveredDelivery.attemptCount >= 2, 'Recovered delivery did not record a retry');
  assert(!recoveredDelivery.errorCode && !recoveredDelivery.message, 'Recovered delivery retained stale error details');

  const telemetryBeforeReplay = await admin.searchEvents({agentId});
  await runAgent(agentId, username, password, agentData);
  const telemetryAfterReplay = await admin.searchEvents({agentId});
  assert(telemetryAfterReplay.items.length === telemetryBeforeReplay.items.length, 'Succeeded delivery was executed more than once');

  await runCompose('restart', 'postgres');
  await runCompose('up', '-d', '--wait', 'postgres');
  await runCompose('restart', 'control-service');
  await waitForHealth();

  await runCli(['skill', 'revoke', '--assignment-id', String(assignment.id)]);
  const removeEvent = await admin.createControlEvent({
    type: 'skill.manifest.changed', scope: {type: 'agent', id: agentId},
    resource: {type: 'skill', id: skillId, revision: '2'}, task: {type: 'skill.reconcile'},
    expiresAt: new Date(Date.now() + 60_000).toISOString(), supersedesKey: `skill:${skillId}:${agentId}`,
  });
  await runAgent(agentId, username, password, agentData);
  assert(!fs.existsSync(path.join(agentData, 'managed-skills', skillId)), 'Revoked managed Skill still exists');
  await assertDelivery(admin, String(removeEvent.eventId), 'succeeded');

  const expiredEvent = await admin.createControlEvent({
    type: 'skill.manifest.changed', scope: {type: 'agent', id: agentId},
    resource: {type: 'skill', id: skillId, revision: 'expired'}, task: {type: 'skill.reconcile'},
    expiresAt: new Date(Date.now() + 1_000).toISOString(), supersedesKey: `expired:${skillId}:${agentId}`,
  });
  await new Promise(resolve => setTimeout(resolve, 1_500));
  await runAgent(agentId, username, password, agentData);
  await assertDelivery(admin, String(expiredEvent.eventId), 'expired');

  const telemetryClient = new AepClient({baseUrl, agentId, agentVersion: 'e2e', platform: platform(), tokenStore: new MemoryTokenStore()});
  await telemetryClient.loginWithPassword({enterpriseId: 'demo', username, password});
  const duplicateEventId = `duplicate-${runId}`;
  const duplicateTelemetry = {eventId: duplicateEventId, type: 'e2e.duplicate', occurredAt: new Date().toISOString(), result: 'succeeded', data: {}};
  await telemetryClient.uploadEventBatch([duplicateTelemetry, duplicateTelemetry]);
  await telemetryClient.uploadEventBatch([duplicateTelemetry]);

  const agent = await admin.getAgent(agentId);
  assert((agent.installedSkillIds ?? []).length === 0, 'Agent state still reports an installed Skill');
  const audit = await admin.searchEvents({agentId});
  assert(Array.isArray(audit.items) && audit.items.length >= 3, 'Expected Skill telemetry was not recorded');
  assert(audit.items.filter(item => item.eventId === duplicateEventId).length === 1, 'Telemetry eventId was not deduplicated');
  await runCli(['metadata']);
}

async function runCli(args) {
  const output = await commandOutput('go', [
    'run', './cmd/aepctl',
    '--base-url', baseUrl,
    '--enterprise', 'demo',
    '--username', 'admin',
    '--password', 'change-this-admin-password',
    '--agent-id', `e2e-cli-${runId}`,
    ...args,
  ]);
  if (!output) return null;
  try { return JSON.parse(output); } catch { return output; }
}

async function runCompose(...args) {
  await command('docker', ['compose', '-p', project, '-f', composeFile, ...args], composeEnv);
}

async function waitForSkillInstall(agentId, username, password, dataDirectory, installedSkill) {
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    await runAgent(agentId, username, password, dataDirectory);
    if (fs.existsSync(installedSkill)) return;
    await new Promise(resolve => setTimeout(resolve, 500));
  }
  throw new Error('Skill was not installed after MinIO recovered');
}

async function runAgent(agentId, username, password, dataDirectory) {
  await command(process.execPath, [path.join(root, 'examples', 'node-agent', 'dist', 'index.js'), 'once'], {
    AEP_BASE_URL: baseUrl,
    AEP_ENTERPRISE_ID: 'demo',
    AEP_USERNAME: username,
    AEP_PASSWORD: password,
    AEP_AGENT_ID: agentId,
    AEP_AGENT_DATA_DIR: dataDirectory,
  });
}

async function assertDelivery(admin, eventId, expectedState) {
  const deliveries = await admin.listControlEventDeliveries(eventId);
  assert(Array.isArray(deliveries.items) && deliveries.items.length === 1, `Expected one delivery for ${eventId}`);
  assert(deliveries.items[0].state === expectedState, `Delivery ${eventId} was ${deliveries.items[0].state}`);
  return deliveries.items[0];
}

async function waitForHealth() {
  const deadline = Date.now() + 120_000;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(`${baseUrl}/healthz`);
      if (response.ok) return;
    } catch {}
    await new Promise(resolve => setTimeout(resolve, 1_000));
  }
  throw new Error('Control service did not become healthy within 120 seconds');
}

function createSkillArchive() {
  return new Promise((resolve, reject) => {
    const archive = new yazl.ZipFile();
    const chunks = [];
    archive.outputStream.on('data', chunk => chunks.push(Buffer.from(chunk)));
    archive.outputStream.on('error', reject);
    archive.outputStream.on('end', () => resolve(new Uint8Array(Buffer.concat(chunks))));
    archive.addBuffer(Buffer.from('---\nname: E2E Review\ndescription: M0 test Skill\n---\n\nReview the supplied content.'), 'SKILL.md');
    archive.end();
  });
}

function commandOutput(executable, args, extraEnv = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {cwd: root, env: {...process.env, ...extraEnv}, stdio: ['ignore', 'pipe', 'pipe'], shell: false});
    const stdout = [];
    const stderr = [];
    child.stdout.on('data', chunk => stdout.push(Buffer.from(chunk)));
    child.stderr.on('data', chunk => stderr.push(Buffer.from(chunk)));
    child.on('error', reject);
    child.on('exit', code => {
      const output = Buffer.concat(stdout).toString('utf8').trim();
      const errors = Buffer.concat(stderr).toString('utf8').trim();
      if (code === 0) resolve(output);
      else reject(new Error(`${executable} ${args.join(' ')} exited with ${code}: ${errors}`));
    });
  });
}

function command(executable, args, extraEnv = {}, allowFailure = false) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {cwd: root, env: {...process.env, ...extraEnv}, stdio: 'inherit', shell: false});
    child.on('error', reject);
    child.on('exit', code => {
      if (code === 0 || allowFailure) resolve();
      else reject(new Error(`${executable} ${args.join(' ')} exited with ${code}`));
    });
  });
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function platform() {
  if (process.platform === 'win32') return 'windows';
  if (process.platform === 'darwin') return 'macos';
  return 'linux';
}

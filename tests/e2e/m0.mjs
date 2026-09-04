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
const composeEnv = {
  AEP_PORT: port,
  AEP_MINIO_CONSOLE_PORT: process.env.AEP_E2E_MINIO_CONSOLE_PORT ?? '19001',
  AEP_LOGIN_FAILURE_LIMIT: '3',
  AEP_LOGIN_BACKOFF_BASE: '1s',
  AEP_LOGIN_BACKOFF_MAX: '2s',
};
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
  const admin = new AepClient({baseUrl, tokenStore: new MemoryTokenStore()});
  await admin.loginWithPassword({deploymentId: 'demo', username: 'admin', password: 'change-this-admin-password'});

  await assertUserSessionLogin();
  const sessions = await admin.listUserSessions();
  assert(Array.isArray(sessions.items) && sessions.items.length >= 1, 'Admin session inventory was empty');
  await assertMultiTerminalControlEvent();

  await assertPasswordSecurity();

  const username = `user-${runId}`;
  const password = 'temporary-password-123';
  const user = await runCli(['user', 'create', '--user', username, '--display-name', `E2E User ${runId}`, '--temporary-password', password, '--require-password-change=false', '--role-id', 'admin', '--team-id', 'all-users']);
  const skillId = `review-${runId}`;
  const archivePath = path.join(tempDirectory, `${skillId}.zip`);
  fs.writeFileSync(archivePath, await createSkillArchive());
  await runCli(['skill', 'create', '--skill-id', skillId, '--name', `Review ${runId}`, '--description', 'M0 end-to-end Skill']);
  await runCli(['skill', 'upload', '--skill-id', skillId, '--version', '1.0.0', '--file', archivePath]);
  await runCli(['skill', 'publish', '--skill-id', skillId, '--version', '1.0.0']);
  const assignment = await runCli(['skill', 'assign', '--skill-id', skillId, '--subject-type', 'user', '--subject-id', String(user.id)]);

  const agentData = path.join(tempDirectory, 'agent');
  const installEventId = await automaticSkillEventId(skillId, 'assigned:');
  const installedSkill = path.join(agentData, 'managed-skills', skillId, 'SKILL.md');
  await runCompose('stop', 'minio');
  await runAgent(username, password, agentData);
  assert(!fs.existsSync(installedSkill), 'Skill was installed while MinIO was unavailable');
  await assertDelivery(admin, installEventId, 'failed');

  await runCompose('start', 'minio');
  await waitForSkillInstall(username, password, agentData, installedSkill);
  const recoveredDelivery = await assertDelivery(admin, installEventId, 'succeeded');
  assert(recoveredDelivery.attemptCount >= 2, 'Recovered delivery did not record a retry');
  assert(!recoveredDelivery.errorCode && !recoveredDelivery.message, 'Recovered delivery retained stale error details');

  const telemetryBeforeReplay = await admin.searchEvents({userId: String(user.id)});
  await runAgent(username, password, agentData);
  const telemetryAfterReplay = await admin.searchEvents({userId: String(user.id)});
  assert(telemetryAfterReplay.items.length === telemetryBeforeReplay.items.length, 'Succeeded delivery was executed more than once');

  await runCompose('restart', 'postgres');
  await runCompose('up', '-d', '--wait', 'postgres');
  await runCompose('restart', 'control-service');
  await waitForHealth();

  await runCli(['skill', 'revoke', '--assignment-id', String(assignment.id)]);
  const removeEventId = await automaticSkillEventId(skillId, 'revoked:');
  await runAgent(username, password, agentData);
  assert(!fs.existsSync(path.join(agentData, 'managed-skills', skillId)), 'Revoked managed Skill still exists');
  await assertDelivery(admin, removeEventId, 'succeeded');

  const expiredEvent = await admin.createControlEvent({
    type: 'skill.manifest.changed', scope: {type: 'user', id: String(user.id)},
    resource: {type: 'skill', id: skillId, revision: 'expired'}, task: {type: 'skill.reconcile'},
    expiresAt: new Date(Date.now() + 1_000).toISOString(), supersedesKey: `expired:${skillId}:${user.id}`,
  });
  await new Promise(resolve => setTimeout(resolve, 1_500));
  await runAgent(username, password, agentData);
  await assertDelivery(admin, String(expiredEvent.eventId), 'expired');

  const telemetryClient = new AepClient({baseUrl, tokenStore: new MemoryTokenStore()});
  await telemetryClient.loginWithPassword({deploymentId: 'demo', username, password});
  const duplicateEventId = `duplicate-${runId}`;
  const duplicateTelemetry = {eventId: duplicateEventId, type: 'e2e.duplicate', occurredAt: new Date().toISOString(), result: 'succeeded', data: {}};
  await telemetryClient.uploadEventBatch([duplicateTelemetry, duplicateTelemetry]);
  await telemetryClient.uploadEventBatch([duplicateTelemetry]);

  const audit = await admin.searchEvents({userId: String(user.id)});
  assert(Array.isArray(audit.items) && audit.items.length >= 3, 'Expected Skill telemetry was not recorded');
  assert(audit.items.filter(item => item.eventId === duplicateEventId).length === 1, 'Telemetry eventId was not deduplicated');
  await runCli(['metadata']);
}

async function assertUserSessionLogin() {
  const response = await fetch(`${baseUrl}/aep/v1/auth/password/login`, {
    method: 'POST',
    headers: {'Content-Type': 'application/json', 'X-AEP-Protocol-Version': '1.0'},
    body: JSON.stringify({deploymentId: 'demo', username: 'admin', password: 'change-this-admin-password'}),
  });
  assert(response.status === 200, `Deployment-only login returned ${response.status}`);
  const tokens = await response.json();
  assert(tokens.sessionId, 'Deployment-only login did not issue a session ID');
  const claims = decodeJwtPayload(tokens.accessToken);
  assert(claims.deployment_id === 'demo', 'Session access token omitted deployment_id');
  assert(claims.session_id === tokens.sessionId, 'Session access token omitted the issued session ID');
  assert(!Object.hasOwn(claims, 'agent_id'), 'User session token unexpectedly contains agent_id');
  const stored = Number(await queryDatabase(`SELECT count(*) FROM user_sessions WHERE session_id='${tokens.sessionId}' AND topic='user:demo:${claims.sub}'`));
  assert(stored === 1, 'User session was not persisted with its user topic');
}

async function assertMultiTerminalControlEvent() {
  const first = new AepClient({baseUrl, tokenStore: new MemoryTokenStore()});
  const second = new AepClient({baseUrl, tokenStore: new MemoryTokenStore()});
  const firstTokens = await first.loginWithPassword({deploymentId: 'demo', username: 'admin', password: 'change-this-admin-password'});
  await second.loginWithPassword({deploymentId: 'demo', username: 'admin', password: 'change-this-admin-password'});
  const userId = decodeJwtPayload(firstTokens.accessToken).sub;
  const event = await first.createControlEvent({
    type: 'model.catalog.changed',
    scope: {type: 'user', id: userId},
    task: {type: 'model.reconcile'},
    expiresAt: new Date(Date.now() + 60_000).toISOString(),
  });
  const firstPage = await first.listControlEvents(undefined, 10);
  const secondPage = await second.listControlEvents(undefined, 10);
  assert(firstPage.items.length === 1 && secondPage.items.length === 1, 'User event was not delivered to both sessions');
  assert(firstPage.items[0].deliveryId !== secondPage.items[0].deliveryId, 'Terminals shared a delivery ID');
  await first.acknowledgeControlEvent(firstPage.items[0].deliveryId, new Date().toISOString());
  await first.reportControlEventResult(firstPage.items[0].deliveryId, {status: 'succeeded', completedAt: new Date().toISOString()});
  const secondAgain = await second.listControlEvents(undefined, 10);
  assert(secondAgain.items.length === 1, 'Acknowledging one terminal consumed the other terminal delivery');
  const deliveries = await first.listControlEventDeliveries(event.eventId);
  const sessionDeliveries = deliveries.items.filter(item => item.sessionId);
  assert(sessionDeliveries.length >= 2, 'Admin delivery query did not include both sessions');
}

function decodeJwtPayload(token) {
  const encoded = token.split('.')[1];
  assert(encoded, 'Access token payload is missing');
  return JSON.parse(Buffer.from(encoded, 'base64url').toString('utf8'));
}

async function automaticSkillEventId(skillId, revisionPrefix) {
  const eventId = await queryDatabase(`SELECT event_id FROM control_events WHERE resource_id='${skillId}' AND resource_revision LIKE '${revisionPrefix}%' ORDER BY created_at DESC LIMIT 1`);
  assert(eventId, `Automatic ${revisionPrefix} Skill event was not created`);
  return eventId;
}

async function assertPasswordSecurity() {
  const username = `forced-change-${runId}`;
  const temporaryPassword = 'temporary-password-123';
  const changedPassword = 'changed-password-456';
  await runCli(['user', 'create', '--user', username, '--display-name', `Forced Change ${runId}`, '--temporary-password', temporaryPassword, '--role-id', 'admin', '--team-id', 'all-users']);

  const store = new MemoryTokenStore();
  const client = new AepClient({baseUrl, tokenStore: store});
  const restricted = await client.loginWithPassword({deploymentId: 'demo', username, password: temporaryPassword});
  assert(restricted.passwordChangeRequired === true, 'Temporary-password login was not marked as restricted');
  const identity = await client.getCurrentIdentity();
  assert(identity.passwordChangeRequired === true && identity.sessionExpiresAt, 'Restricted identity state was incomplete');
  await assertProblem(client.listModels(), 'PASSWORD_CHANGE_REQUIRED');

  const changed = await client.changePassword(temporaryPassword, changedPassword);
  assert(changed.passwordChangeRequired === false, 'Password change did not rotate to an unrestricted session');
  await client.listModels();
  await client.logout();

  for (let attempt = 1; attempt <= 3; attempt += 1) {
    const response = await passwordLogin(username, 'incorrect-password-123');
    assert(response.status === (attempt < 3 ? 401 : 429), `Login attempt ${attempt} returned ${response.status}`);
    if (attempt === 3) assert(response.headers.get('retry-after') === '1', 'Rate-limited response omitted Retry-After');
  }
  const blocked = await passwordLogin(username, changedPassword);
  assert(blocked.status === 429, 'Active login backoff accepted valid credentials');
  await new Promise(resolve => setTimeout(resolve, 1_100));
  const recovered = await passwordLogin(username, changedPassword);
  assert(recovered.status === 200, `Login did not recover after backoff: ${recovered.status}`);

  const auditCount = Number(await queryDatabase(`SELECT count(*) FROM authentication_audit_events WHERE deployment_id='demo' AND user_id IN (SELECT id FROM users WHERE username='${username}')`));
  assert(auditCount >= 6, `Authentication audit recorded only ${auditCount} events`);
}

async function passwordLogin(username, password) {
  return fetch(`${baseUrl}/aep/v1/auth/password/login`, {
    method: 'POST',
    headers: {'Content-Type': 'application/json', 'X-AEP-Protocol-Version': '1.0'},
    body: JSON.stringify({deploymentId: 'demo', username, password}),
  });
}

async function queryDatabase(sql) {
  return commandOutput('docker', ['compose', '-p', project, '-f', composeFile, 'exec', '-T', 'postgres', 'psql', '-U', 'aep', '-d', 'aep', '-Atc', sql], composeEnv);
}

async function assertProblem(promise, code) {
  try {
    await promise;
  } catch (error) {
    assert(error?.code === code, `Expected ${code}, got ${error?.code ?? error}`);
    return;
  }
  throw new Error(`Expected ${code}`);
}

async function runCli(args) {
  const output = await commandOutput('go', [
    'run', './cmd/aepctl',
    '--base-url', baseUrl,
    '--deployment', 'demo',
    '--username', 'admin',
    '--password', 'change-this-admin-password',
    ...args,
  ]);
  if (!output) return null;
  try { return JSON.parse(output); } catch { return output; }
}

async function runCompose(...args) {
  await command('docker', ['compose', '-p', project, '-f', composeFile, ...args], composeEnv);
}

async function waitForSkillInstall(username, password, dataDirectory, installedSkill) {
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    await runAgent(username, password, dataDirectory);
    if (fs.existsSync(installedSkill)) return;
    await new Promise(resolve => setTimeout(resolve, 500));
  }
  throw new Error('Skill was not installed after MinIO recovered');
}

async function runAgent(username, password, dataDirectory) {
  await command(process.execPath, [path.join(root, 'examples', 'node-agent', 'dist', 'index.js'), 'once'], {
    AEP_BASE_URL: baseUrl,
    AEP_DEPLOYMENT_ID: 'demo',
    AEP_USERNAME: username,
    AEP_PASSWORD: password,
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

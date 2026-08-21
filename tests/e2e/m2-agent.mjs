import {spawn} from 'node:child_process';
import fs from 'node:fs';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

import {AepClient, MemoryTokenStore} from '../../packages/aep-sdk-node/dist/index.js';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const composeFile = path.join(root, 'deploy', 'compose', 'compose.yaml');
const agentEntry = path.join(root, 'examples', 'node-agent', 'dist', 'index.js');
const project = 'aep-m2-agent-e2e';
const controlPort = process.env.AEP_M2_AGENT_CONTROL_PORT ?? '18086';
const controlBaseUrl = 'http://localhost:' + controlPort;
const dataDirectory = fs.mkdtempSync(path.join(os.tmpdir(), 'aep-m2-agent-'));
const composeEnv = {
  AEP_PORT: controlPort,
  AEP_MINIO_CONSOLE_PORT: process.env.AEP_M2_AGENT_MINIO_CONSOLE_PORT ?? '19006',
};
const runId = Date.now().toString(36);
let consumer;

try {
  await compose('up', '-d', '--build');
  await waitForHealth(controlBaseUrl + '/healthz', 180_000);
  await runScenario();
  console.log('AEP M2 reference Agent Credential scenario passed.');
} catch (error) {
  await compose('logs', '--no-color', '--tail=200', 'control-service', 'postgres', true);
  throw error;
} finally {
  if (consumer) await new Promise(resolve => consumer.close(resolve));
  await compose('down', '-v', '--remove-orphans', true);
  fs.rmSync(dataDirectory, {recursive: true, force: true});
}

async function runScenario() {
  const admin = new AepClient({
    baseUrl: controlBaseUrl,
    agentId: 'm2-agent-admin-' + runId,
    agentVersion: 'e2e',
    platform: platform(),
    tokenStore: new MemoryTokenStore(),
  });
  await admin.loginWithPassword({
    enterpriseId: 'demo',
    username: 'admin',
    password: 'change-this-admin-password',
  });

  const username = 'm2-agent-user-' + runId;
  const password = 'temporary-password-123';
  const agentId = 'm2-reference-agent-' + runId;
  const user = await admin.createUser({
    enterpriseId: 'demo',
    username,
    displayName: 'M2 Reference Agent ' + runId,
    temporaryPassword: password,
    requirePasswordChange: false,
    organizationIds: [],
    roleIds: [],
  });
  const firstSecret = 'agent-credential-first-' + runId;
  const rotatedSecret = 'agent-credential-rotated-' + runId;
  const serverOnlySecret = 'agent-server-only-' + runId;
  const credential = await admin.createCredential({
    name: 'Reference Agent HTTP',
    service: 'e2e-protected-service',
    type: 'api_key',
    deliveryMode: 'agent',
    value: firstSecret,
    enabled: true,
  });
  const assignment = await admin.createCredentialAssignment({
    credentialId: credential.id,
    subject: {type: 'agent', id: agentId},
  });
  const serverOnly = await admin.createCredential({
    name: 'Reference Agent server-only',
    service: 'e2e-protected-service',
    type: 'api_key',
    deliveryMode: 'server_only',
    value: serverOnlySecret,
    enabled: true,
  });
  await admin.createCredentialAssignment({
    credentialId: serverOnly.id,
    subject: {type: 'user', id: user.id},
  });

  let expectedSecret = firstSecret;
  const requests = [];
  const consumerUrl = await startConsumer((authorization) => {
    requests.push(authorization);
    return authorization === 'Bearer ' + expectedSecret;
  });
  const sharedEnv = {
    AEP_BASE_URL: controlBaseUrl,
    AEP_ENTERPRISE_ID: 'demo',
    AEP_USERNAME: username,
    AEP_PASSWORD: password,
    AEP_AGENT_ID: agentId,
    AEP_AGENT_DATA_DIR: dataDirectory,
    AEP_CREDENTIAL_URL: consumerUrl,
    AEP_CREDENTIAL_CACHE_MS: '1000',
  };

  const first = await runAgent({...sharedEnv, AEP_CREDENTIAL_ID: credential.id});
  assertSuccess(first, credential.id, 204);
  assert(!containsAnySecret(first, [firstSecret, rotatedSecret, serverOnlySecret]), 'Initial Agent output leaked Credential material');

  const unavailable = await runAgent({...sharedEnv, AEP_CREDENTIAL_ID: serverOnly.id});
  assert(unavailable.code !== 0, 'server_only Credential unexpectedly reached the Agent consumer');
  assert(!containsAnySecret(unavailable, [firstSecret, rotatedSecret, serverOnlySecret]), 'server_only failure leaked Credential material');

  await admin.rotateCredential(credential.id, {value: rotatedSecret});
  expectedSecret = rotatedSecret;
  const rotated = await runAgent({...sharedEnv, AEP_CREDENTIAL_ID: credential.id});
  assertSuccess(rotated, credential.id, 204);
  assert(requests.at(-1) === 'Bearer ' + rotatedSecret, 'Restarted Agent did not converge on rotated material');

  await admin.deleteCredentialAssignment(assignment.id);
  const revoked = await runAgent({...sharedEnv, AEP_CREDENTIAL_ID: credential.id});
  assert(revoked.code !== 0, 'Revoked Credential remained usable by the Agent');
  assert(requests.length === 2, 'Revoked or server_only Credential reached the protected service');

  for (const secret of [firstSecret, rotatedSecret, serverOnlySecret]) {
    assert(!directoryContains(dataDirectory, secret), 'Agent state persisted Credential material');
  }
  const telemetry = await admin.searchEvents({agentId});
  const serializedTelemetry = JSON.stringify(telemetry);
  for (const secret of [firstSecret, rotatedSecret, serverOnlySecret]) {
    assert(!serializedTelemetry.includes(secret), 'Telemetry persisted Credential material');
  }
  const items = Array.isArray(telemetry.items) ? telemetry.items : [];
  assert(items.filter(item => item.type === 'credential.use.completed').length === 2, 'Expected two successful Credential-use telemetry events');
  assert(items.filter(item => item.type === 'credential.use.failed').length === 2, 'Expected two failed Credential-use telemetry events');
}

function runAgent(extraEnv) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [agentEntry, 'credential'], {
      cwd: root,
      env: {...process.env, ...extraEnv},
      shell: false,
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let stdout = '';
    let stderr = '';
    child.stdout.setEncoding('utf8');
    child.stderr.setEncoding('utf8');
    child.stdout.on('data', chunk => {
      stdout += chunk;
    });
    child.stderr.on('data', chunk => {
      stderr += chunk;
    });
    child.on('error', reject);
    child.on('exit', code => resolve({code: code ?? -1, stdout, stderr}));
  });
}

function assertSuccess(result, credentialId, status) {
  assert(result.code === 0, 'Agent Credential command failed: ' + result.stderr);
  const lines = result.stdout.trim().split(/\r?\n/).filter(Boolean);
  assert(lines.length === 1, 'Agent emitted unexpected output: ' + result.stdout);
  const output = JSON.parse(lines[0]);
  assert(output.credentialId === credentialId && output.status === status, 'Agent emitted an invalid safe result');
  assert(output.service === 'e2e-protected-service', 'Agent omitted safe Credential service metadata');
}

function startConsumer(authorize) {
  consumer = http.createServer((request, response) => {
    if (!authorize(request.headers.authorization)) {
      response.writeHead(401).end();
      return;
    }
    response.writeHead(204).end();
  });
  return new Promise((resolve, reject) => {
    consumer.once('error', reject);
    consumer.listen(0, '127.0.0.1', () => {
      const address = consumer.address();
      if (!address || typeof address === 'string') return reject(new Error('Credential consumer did not bind a TCP port'));
      resolve('http://127.0.0.1:' + address.port + '/protected');
    });
  });
}

function directoryContains(directory, value) {
  return fs.readdirSync(directory, {withFileTypes: true}).some(entry => {
    const target = path.join(directory, entry.name);
    return entry.isDirectory() ? directoryContains(target, value) : fs.readFileSync(target).includes(Buffer.from(value));
  });
}

function containsAnySecret(result, secrets) {
  const output = result.stdout + '\n' + result.stderr;
  return secrets.some(secret => output.includes(secret));
}

async function waitForHealth(url, timeout) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url);
      if (response.ok) return;
    } catch {}
    await new Promise(resolve => setTimeout(resolve, 1_000));
  }
  throw new Error(url + ' did not become healthy within ' + timeout + 'ms');
}

function compose(...args) {
  let allowFailure = false;
  if (args.at(-1) === true) {
    allowFailure = true;
    args.pop();
  }
  return command('docker', ['compose', '-p', project, '-f', composeFile, ...args], composeEnv, allowFailure);
}

function command(executable, args, extraEnv = {}, allowFailure = false) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {
      cwd: root,
      env: {...process.env, ...extraEnv},
      stdio: 'inherit',
      shell: false,
    });
    child.on('error', reject);
    child.on('exit', code => {
      if (code === 0 || allowFailure) resolve();
      else reject(new Error(executable + ' ' + args.join(' ') + ' exited with ' + code));
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

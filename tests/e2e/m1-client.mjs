import {spawn} from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

import {AepClient, MemoryTokenStore} from '../../packages/aep-sdk-node/dist/index.js';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const composeFiles = [
  path.join(root, 'deploy', 'compose', 'compose.yaml'),
  path.join(root, 'deploy', 'compose', 'gateway.yaml'),
];
const agentEntry = path.join(root, 'examples', 'node-agent', 'dist', 'index.js');
const project = 'aep-m1-client-e2e';
const controlPort = process.env.AEP_M1_CLIENT_CONTROL_PORT ?? '18084';
const gatewayPort = process.env.AEP_M1_CLIENT_GATEWAY_PORT ?? '19081';
const controlBaseUrl = 'http://localhost:' + controlPort;
const gatewayBaseUrl = 'http://localhost:' + gatewayPort + '/v1';
const dataDirectory = fs.mkdtempSync(path.join(os.tmpdir(), 'aep-m1-client-'));
const composeEnv = {
  AEP_PORT: controlPort,
  AEP_GATEWAY_PORT: gatewayPort,
  AEP_MINIO_CONSOLE_PORT: process.env.AEP_M1_CLIENT_MINIO_CONSOLE_PORT ?? '19004',
};
const runId = Date.now().toString(36);

try {
  await compose('up', '-d', '--build');
  await Promise.all([
    waitForHealth(controlBaseUrl + '/healthz', 180_000),
    waitForHealth('http://localhost:' + gatewayPort + '/healthz', 180_000),
  ]);
  await runScenario();
  console.log('AEP M1 real Agent client scenario passed.');
} catch (error) {
  await compose('logs', '--no-color', '--tail=200', 'control-service', 'higress', 'gateway-authorizer', 'mock-openai', true);
  throw error;
} finally {
  await compose('down', '-v', '--remove-orphans', true);
  fs.rmSync(dataDirectory, {recursive: true, force: true});
}

async function runScenario() {
  const admin = new AepClient({
    baseUrl: controlBaseUrl,
    tokenStore: new MemoryTokenStore(),
  });
  await admin.loginWithPassword({
    deploymentId: 'demo',
    username: 'admin',
    password: 'change-this-admin-password',
  });

  const modelCredential = await admin.createCredential({
    name: 'M1 client provider', service: 'mock-openai', type: 'api_key',
    deliveryMode: 'server_only', value: 'm1-e2e-provider-secret', enabled: true,
  });

  const username = 'client-user-' + runId;
  const password = 'temporary-password-123';
  const user = await admin.createUser({
    deploymentId: 'demo',
    username,
    displayName: 'Real Client User ' + runId,
    temporaryPassword: password,
    requirePasswordChange: false,
    teamIds: ['all-users'],
    roleIds: ['admin'],
  });
  await admin.createModel({
    id: 'enterprise-chat',
    displayName: 'Enterprise Chat',
    sourceType: 'gateway',
    protocol: 'openai-compatible',
    endpoint: gatewayBaseUrl,
    upstreamModel: 'mock-upstream-chat',
    credentialId: modelCredential.id,
    capabilities: ['text', 'streaming'],
    contextWindow: 32768,
    isDefault: true,
    enabled: true,
  });
  await admin.createModelAssignment({
    modelId: 'enterprise-chat',
    subject: {type: 'user', id: user.id},
  });

  const sharedEnv = {
    AEP_BASE_URL: controlBaseUrl,
    AEP_DEPLOYMENT_ID: 'demo',
    AEP_USERNAME: username,
    AEP_PASSWORD: password,
    AEP_AGENT_DATA_DIR: dataDirectory,
    AEP_MODEL_TIMEOUT_MS: '10000',
  };

  const completion = await runAgent({...sharedEnv, AEP_CHAT_PROMPT: 'hello from the real Agent'});
  assert(completion.code === 0, 'Non-streaming Agent command failed: ' + completion.stderr);
  const completionBody = parseAgentOutput(completion.stdout);
  assert(completionBody.modelId === 'enterprise-chat', 'Agent did not select the default enterprise model');
  assert(completionBody.responseModel === 'mock-upstream-chat', 'Agent did not receive the rewritten upstream model');
  assert(completionBody.content === 'Hello AEP' && completionBody.streamed === false, 'Unexpected non-streaming Agent result');
  assert(!containsSecret(completion), 'Provider credential was exposed to the Agent process');

  const streaming = await runAgent({
    ...sharedEnv,
    AEP_CHAT_PROMPT: 'stream from the real Agent',
    AEP_MODEL_ID: 'enterprise-chat',
    AEP_CHAT_STREAM: 'true',
  });
  assert(streaming.code === 0, 'Streaming Agent command failed: ' + streaming.stderr);
  const streamingBody = parseAgentOutput(streaming.stdout);
  assert(streamingBody.modelId === 'enterprise-chat', 'Agent did not use the explicit enterprise model');
  assert(streamingBody.responseModel === 'mock-upstream-chat', 'Streaming response model was not rewritten');
  assert(streamingBody.content === 'Hello AEP' && streamingBody.streamed === true, 'Agent did not aggregate streaming chunks');
  assert(!containsSecret(streaming), 'Provider credential was exposed in the streaming Agent process');

  const failure = await runAgent({
    ...sharedEnv,
    AEP_CHAT_PROMPT: 'force upstream failure',
    AEP_MODEL_ID: 'enterprise-chat',
  });
  assert(failure.code !== 0, 'Failed upstream request unexpectedly succeeded');
  assert(!containsSecret(failure), 'Provider credential was exposed in the failed Agent process');

  const telemetry = await admin.searchEvents({userId: user.id});
  const items = Array.isArray(telemetry.items) ? telemetry.items : [];
  const completed = items.filter(item => item.type === 'model.request.completed');
  const failed = items.filter(item => item.type === 'model.request.failed');
  assert(completed.length === 2, 'Expected two completed model telemetry events, got ' + completed.length);
  assert(failed.length === 1, 'Expected one failed model telemetry event, got ' + failed.length);
  assert(completed.every(item => item.result === 'success' && item.resourceId === 'enterprise-chat'), 'Completed telemetry has invalid result or model resource');
  assert(failed[0].result === 'failure' && failed[0].data?.status === 503, 'Failure telemetry did not retain the safe HTTP status');
  const serializedTelemetry = JSON.stringify(items);
  for (const forbidden of [
    'm1-e2e-provider-secret',
    'hello from the real Agent',
    'stream from the real Agent',
    'force upstream failure',
  ]) {
    assert(!serializedTelemetry.includes(forbidden), 'Telemetry exposed sensitive request data: ' + forbidden);
  }
}

function runAgent(extraEnv) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [agentEntry, 'chat'], {
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

function parseAgentOutput(stdout) {
  const lines = stdout.trim().split(/\r?\n/).filter(Boolean);
  if (lines.length !== 1) throw new Error('Expected one JSON line from Agent, got: ' + stdout);
  return JSON.parse(lines[0]);
}

function containsSecret(result) {
  return (result.stdout + '\n' + result.stderr).includes('m1-e2e-provider-secret');
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
  const composeArgs = ['compose', '-p', project];
  for (const file of composeFiles) composeArgs.push('-f', file);
  composeArgs.push(...args);
  return command('docker', composeArgs, composeEnv, allowFailure);
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

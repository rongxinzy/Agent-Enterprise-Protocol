import {spawn} from 'node:child_process';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

import {AepClient, MemoryTokenStore} from '../../packages/aep-sdk-node/dist/index.js';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const composeFiles = [
  path.join(root, 'deploy', 'compose', 'compose.yaml'),
  path.join(root, 'deploy', 'compose', 'gateway.yaml'),
];
const project = 'aep-m1-gateway-e2e';
const controlPort = process.env.AEP_M1_GATEWAY_CONTROL_PORT ?? '18083';
const gatewayPort = process.env.AEP_M1_GATEWAY_PORT ?? '19080';
const controlBaseUrl = 'http://localhost:' + controlPort;
const gatewayBaseUrl = 'http://localhost:' + gatewayPort + '/v1';
const composeEnv = {
  AEP_PORT: controlPort,
  AEP_GATEWAY_PORT: gatewayPort,
  AEP_MINIO_CONSOLE_PORT: process.env.AEP_M1_GATEWAY_MINIO_CONSOLE_PORT ?? '19003',
  AEP_MODEL_ACCESS_TTL: process.env.AEP_M1_GATEWAY_TOKEN_TTL ?? '8s',
};
const runId = Date.now().toString(36);

try {
  await compose('up', '-d', '--build');
  await Promise.all([
    waitForHealth(controlBaseUrl + '/healthz', 180_000),
    waitForHealth('http://localhost:' + gatewayPort + '/healthz', 180_000),
  ]);
  await runScenario();
  console.log('AEP M1 Higress gateway scenario passed.');
} catch (error) {
  await compose('logs', '--no-color', '--tail=200', 'higress', 'gateway-authorizer', 'mock-openai', true);
  throw error;
} finally {
  await compose('down', '-v', '--remove-orphans', true);
}

async function runScenario() {
  const admin = new AepClient({
    baseUrl: controlBaseUrl, agentId: 'gateway-admin-' + runId, agentVersion: 'e2e',
    platform: platform(), tokenStore: new MemoryTokenStore(),
  });
  await admin.loginWithPassword({enterpriseId: 'demo', username: 'admin', password: 'change-this-admin-password'});
  const modelCredential = await admin.createCredential({
    name: 'M1 gateway provider', service: 'mock-openai', type: 'api_key',
    deliveryMode: 'server_only', value: 'm1-e2e-provider-secret', enabled: true,
  });
  const connection = await admin.getModelConnection();
  assert(connection.baseUrl === gatewayBaseUrl, 'Expected gateway URL ' + gatewayBaseUrl + ', got ' + connection.baseUrl);

  const username = 'gateway-user-' + runId;
  const password = 'temporary-password-123';
  const user = await admin.createUser({
    enterpriseId: 'demo', username, displayName: 'Gateway User ' + runId,
    temporaryPassword: password, requirePasswordChange: false, organizationIds: [], roleIds: [],
  });
  await admin.createModel({
    id: 'enterprise-chat', displayName: 'Enterprise Chat', sourceType: 'gateway',
    protocol: 'openai-compatible', endpoint: gatewayBaseUrl, upstreamModel: 'mock-upstream-chat',
    credentialId: modelCredential.id, capabilities: ['text', 'streaming', 'reasoning'],
    reasoningCompatibility: {
      thinkingFormat: 'deepseek', supportsReasoningEffort: true,
      requiresReasoningContentOnAssistantMessages: true,
    },
    contextWindow: 32768,
    isDefault: true, enabled: true,
  });
  await admin.createModel({
    id: 'unassigned-chat', displayName: 'Unassigned Chat', sourceType: 'gateway',
    protocol: 'openai-compatible', endpoint: gatewayBaseUrl, upstreamModel: 'unassigned-upstream',
    credentialId: null, capabilities: ['text'], contextWindow: 8192,
    isDefault: false, enabled: true,
  });
  await admin.createModelAssignment({modelId: 'enterprise-chat', subject: {type: 'user', id: user.id}});

  const store = new MemoryTokenStore();
  const agent = new AepClient({
    baseUrl: controlBaseUrl, agentId: 'gateway-agent-' + runId, agentVersion: 'e2e',
    platform: platform(), tokenStore: store,
  });
  await agent.loginWithPassword({enterpriseId: 'demo', username, password});
  const tokens = await store.get();
  const modelToken = tokens?.modelAccessToken;
  assert(modelToken, 'Agent login did not return a model access token');

  const completion = await inference(modelToken, {model: 'enterprise-chat', messages: [{role: 'user', content: 'hello'}]});
  assert(completion.response.status === 200, 'Non-streaming inference failed: ' + completion.response.status + ' ' + completion.text);
  const completionBody = JSON.parse(completion.text);
  assert(completionBody.model === 'mock-upstream-chat', 'Higress did not rewrite the enterprise model ID');
  assert(completionBody.choices[0].message.content === 'Hello AEP', 'Unexpected mock completion content');
  assert(completionBody.choices[0].message.reasoning_content === 'Think through the request.', 'Non-stream reasoning_content was lost');
  assert(completion.response.headers.get('x-mock-provider-auth') === 'accepted', 'Higress did not inject the provider credential');
  assert(!containsSecret(completion), 'Provider credentials were exposed in the client response');

  const streaming = await inference(modelToken, {model: 'enterprise-chat', stream: true, messages: [{role: 'user', content: 'stream'}]});
  assert(streaming.response.status === 200, 'Streaming inference failed: ' + streaming.response.status + ' ' + streaming.text);
  assert(streaming.response.headers.get('content-type')?.startsWith('text/event-stream'), 'Streaming response was not SSE');
  assert(streaming.text.includes('Hello') && streaming.text.includes(' AEP') && streaming.text.includes('[DONE]'), 'Streaming chunks were incomplete');
  assert(streaming.text.includes('reasoning_content') && streaming.text.includes('Think through the request.'), 'Streaming reasoning_content was lost');
  assert(!containsSecret(streaming), 'Provider credentials were exposed in the streaming response');

  const replay = await inference(modelToken, {
    model: 'enterprise-chat', thinking: {type: 'enabled'}, reasoning_effort: 'high',
    messages: [
      {role: 'user', content: 'verify reasoning replay'},
      {role: 'assistant', content: null, reasoning_content: 'Call the clock tool.', tool_calls: [{id: 'call-1', type: 'function', function: {name: 'clock', arguments: '{}'}}]},
      {role: 'tool', tool_call_id: 'call-1', content: '12:00'},
    ],
  });
  assert(replay.response.status === 200, 'Reasoning tool replay failed: ' + replay.response.status + ' ' + replay.text);

  await expectGatewayProblem(null, {model: 'enterprise-chat'}, 401, 'TOKEN_INVALID');
  await expectGatewayProblem(modelToken + 'invalid', {model: 'enterprise-chat'}, 401, 'TOKEN_INVALID');
  await expectGatewayProblem(modelToken, {model: 'unassigned-chat'}, 403, 'MODEL_NOT_ALLOWED');

  const expiresAt = decodeJwtPart(modelToken, 1).exp * 1000;
  await new Promise(resolve => setTimeout(resolve, Math.max(0, expiresAt - Date.now() + 250)));
  await expectGatewayProblem(modelToken, {model: 'enterprise-chat'}, 401, 'TOKEN_INVALID');
}

async function inference(token, body) {
  const headers = {'Content-Type': 'application/json', 'X-AEP-Tenant-ID': 'spoofed'};
  if (token) headers.Authorization = 'Bearer ' + token;
  const response = await fetch(gatewayBaseUrl + '/chat/completions', {method: 'POST', headers, body: JSON.stringify(body)});
  return {response, text: await response.text()};
}

async function expectGatewayProblem(token, body, status, code) {
  const result = await inference(token, body);
  let problem;
  try { problem = JSON.parse(result.text); } catch {}
  assert(result.response.status === status && problem?.code === code, 'Expected ' + status + ' ' + code + ', got ' + result.response.status + ' ' + result.text);
}

function containsSecret(result) {
  const headers = [...result.response.headers].map(([name, value]) => name + ':' + value).join('\n');
  return (headers + '\n' + result.text).includes('m1-e2e-provider-secret');
}

function decodeJwtPart(token, index) {
  return JSON.parse(Buffer.from(token.split('.')[index], 'base64url').toString('utf8'));
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
    const child = spawn(executable, args, {cwd: root, env: {...process.env, ...extraEnv}, stdio: 'inherit', shell: false});
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

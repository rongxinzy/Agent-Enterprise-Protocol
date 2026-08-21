import {spawn} from 'node:child_process';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

import {AepClient, MemoryTokenStore} from '../../packages/aep-sdk-node/dist/index.js';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const composeFile = path.join(root, 'deploy', 'compose', 'compose.yaml');
const project = 'aep-m1-control-e2e';
const port = process.env.AEP_M1_CONTROL_E2E_PORT ?? '18082';
const gatewayBaseUrl = process.env.AEP_M1_CONTROL_GATEWAY_URL ?? 'http://localhost:19080/v1';
const baseUrl = `http://localhost:${port}`;
const composeEnv = {
  AEP_PORT: port,
  AEP_MINIO_CONSOLE_PORT: process.env.AEP_M1_CONTROL_MINIO_CONSOLE_PORT ?? '19002',
  AEP_MODEL_GATEWAY_BASE_URL: gatewayBaseUrl,
};
const runId = Date.now().toString(36);

try {
  await command('docker', ['compose', '-p', project, '-f', composeFile, 'up', '-d', '--build'], composeEnv);
  await waitForHealth();
  await runScenario();
  console.log('AEP M1 model control scenario passed.');
} finally {
  await command('docker', ['compose', '-p', project, '-f', composeFile, 'down', '-v', '--remove-orphans'], composeEnv, true);
}

async function runScenario() {
  const adminStore = new MemoryTokenStore();
  const admin = new AepClient({baseUrl, agentId: `m1-admin-${runId}`, agentVersion: 'e2e', platform: platform(), tokenStore: adminStore});
  await admin.loginWithPassword({enterpriseId: 'demo', username: 'admin', password: 'change-this-admin-password'});

  const metadata = await admin.getMetadata();
  assert(metadata.capabilities.includes('model_gateway'), 'Configured model gateway capability was not advertised');
  const connection = await admin.getModelConnection();
  assert(connection.baseUrl === gatewayBaseUrl, 'SDK received the wrong model gateway URL');
  assert(connection.protocol === 'openai-compatible', 'SDK received the wrong model gateway protocol');

  const organizationId = `org-${runId}`;
  const username = `model-user-${runId}`;
  const password = 'temporary-password-123';
  const user = await admin.createUser({
    enterpriseId: 'demo', username, displayName: `Model User ${runId}`,
    temporaryPassword: password, requirePasswordChange: false,
    organizationIds: [organizationId], roleIds: [],
  });
  const agentId = `model-agent-${runId}`;
  const descriptors = [
    {suffix: 'enterprise', subject: {type: 'enterprise', id: 'demo'}},
    {suffix: 'organization', subject: {type: 'organization', id: organizationId}},
    {suffix: 'user', subject: {type: 'user', id: user.id}},
    {suffix: 'agent', subject: {type: 'agent', id: agentId}},
  ];
  const assignments = [];
  for (const [index, descriptor] of descriptors.entries()) {
    descriptor.modelId = `model-${descriptor.suffix}-${runId}`;
    await admin.createModel({
      id: descriptor.modelId,
      displayName: `${descriptor.suffix} model`,
      sourceType: 'gateway',
      protocol: 'openai-compatible',
      endpoint: 'https://models.example.test/v1',
      upstreamModel: `upstream-${descriptor.suffix}`,
      credentialId: index === 0 ? 'credential-placeholder' : null,
      capabilities: ['text', 'streaming', 'text'],
      contextWindow: 32768,
      isDefault: index === 0,
      enabled: true,
    });
    assignments.push(await admin.createModelAssignment({modelId: descriptor.modelId, subject: descriptor.subject}));
  }

  const models = (await admin.listAdminModels()).models;
  assert(models.length === 4, 'Administrator model catalog did not contain four models');
  assert(models.filter(model => model.isDefault).length === 1, 'Enterprise catalog did not enforce one default model');
  assert(models.every(model => model.capabilities.join(',') === 'streaming,text'), 'Model capabilities were not normalized');
  await admin.updateModel(descriptors[0].modelId, {credentialId: null});
  assert((await admin.getModel(descriptors[0].modelId)).credentialId === null, 'Credential reference was not cleared');
  await admin.updateModel(descriptors[1].modelId, {isDefault: true});
  assert((await admin.listAdminModels()).models.filter(model => model.isDefault)[0].id === descriptors[1].modelId, 'Default model did not move atomically');

  await expectProblem(
    admin.createModelAssignment({modelId: descriptors[0].modelId, subject: descriptors[0].subject}),
    409,
    'ASSIGNMENT_EXISTS',
  );
  assert((await admin.listModelAssignments()).assignments.length === 4, 'Assignment list did not contain all four subject types');

  const agentStore = new MemoryTokenStore();
  const agent = new AepClient({baseUrl, agentId, agentVersion: 'e2e', platform: platform(), tokenStore: agentStore});
  await agent.loginWithPassword({enterpriseId: 'demo', username, password});
  const visible = (await agent.listAgentModels()).models;
  assert(visible.length === 4, 'Four-scope authorization union did not expose all models');
  assert(visible.every(model => !Object.hasOwn(model, 'credentialId')), 'Agent model catalog leaked credential metadata');
  await assertModelToken(agentStore, descriptors.map(item => item.modelId), agentId);

  await admin.deleteModelAssignment(assignments[3].id);
  assert((await agent.listAgentModels()).models.length === 3, 'Assignment revocation did not affect real-time discovery');
  assert(modelScopes(await agentStore.get()).length === 4, 'Existing model token changed without rotation');
  await agent.refreshSession();
  await assertModelToken(agentStore, descriptors.slice(0, 3).map(item => item.modelId), agentId);

  await admin.updateModel(descriptors[2].modelId, {enabled: false});
  assert((await agent.listAgentModels()).models.length === 2, 'Disabled model remained discoverable');
  await agent.refreshSession();
  await assertModelToken(agentStore, descriptors.slice(0, 2).map(item => item.modelId), agentId);

  await admin.deleteModel(descriptors[1].modelId);
  assert((await admin.listModelAssignments()).assignments.length === 2, 'Deleting a model did not cascade its assignment');
  assert((await agent.listAgentModels()).models.length === 1, 'Deleted model remained discoverable');

  const cliModels = await commandOutput('go', [
    'run', './cmd/aepctl', '--base-url', baseUrl, '--enterprise', 'demo',
    '--username', 'admin', '--password', 'change-this-admin-password',
    '--agent-id', `m1-cli-${runId}`, 'model', 'list',
  ]);
  assert(JSON.parse(cliModels).models.length === 3, 'aepctl model list did not return the administrator catalog');
}

async function assertModelToken(store, expectedScopes, agentId) {
  const tokens = await store.get();
  assert(tokens?.modelAccessToken, 'Session did not include a model access token');
  const header = decodeJwtPart(tokens.modelAccessToken, 0);
  const claims = decodeJwtPart(tokens.modelAccessToken, 1);
  assert(claims.token_use === 'model', 'model JWT token_use is invalid');
  assert(claims.tenant === 'demo' && claims.agent_id === agentId, 'model JWT identity claims are invalid');
  assert(claims.aud?.includes?.('model-gateway') || claims.aud === 'model-gateway', 'model JWT audience is invalid');
  assert(typeof claims.sub === 'string' && typeof claims.jti === 'string' && claims.iat < claims.exp, 'model JWT registered claims are incomplete');
  assert(JSON.stringify([...claims.model_scopes].sort()) === JSON.stringify([...expectedScopes].sort()), 'model JWT scopes do not match authorization');
  const jwks = await (await fetch(`${baseUrl}/.well-known/jwks.json`)).json();
  assert(jwks.keys.some(key => key.kid === header.kid && key.alg === 'EdDSA'), 'model JWT signing key was not published in JWKS');
}

function modelScopes(tokens) {
  return decodeJwtPart(tokens.modelAccessToken, 1).model_scopes;
}

function decodeJwtPart(token, index) {
  return JSON.parse(Buffer.from(token.split('.')[index], 'base64url').toString('utf8'));
}

async function expectProblem(promise, status, code) {
  try {
    await promise;
  } catch (error) {
    assert(error.status === status && error.code === code, `Expected ${status} ${code}, received ${error.status} ${error.code}`);
    return;
  }
  throw new Error(`Expected ${status} ${code}`);
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

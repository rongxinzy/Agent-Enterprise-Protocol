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
  const admin = new AepClient({baseUrl, tokenStore: adminStore});
  await admin.loginWithPassword({deploymentId: 'demo', username: 'admin', password: 'change-this-admin-password'});
  const modelCredential = await admin.createCredential({
    name: 'M1 control provider', service: 'mock-openai', type: 'api_key',
    deliveryMode: 'server_only', value: 'm1-control-provider-secret', enabled: true,
  });

  const metadata = await admin.getMetadata();
  assert(metadata.capabilities.includes('model_gateway'), 'Configured model gateway capability was not advertised');
  const connection = await admin.getModelConnection();
  assert(connection.baseUrl === gatewayBaseUrl, 'SDK received the wrong model gateway URL');
  assert(connection.protocol === 'openai-compatible', 'SDK received the wrong model gateway protocol');
  await assertDeploymentSettings(admin);

  const username = `model-user-${runId}`;
  const password = 'temporary-password-123';
  const role = await admin.createRole({
    name: `Model Role ${runId}`,
    description: 'M1 model authorization role',
    permissions: [],
  });
  const team = await admin.createTeam({
    name: `Model Team ${runId}`,
    description: 'M1 model authorization team',
  });
  const user = await admin.createUser({
    deploymentId: 'demo', username, displayName: `Model User ${runId}`,
    temporaryPassword: password, requirePasswordChange: false,
    teamIds: [team.id], roleIds: [role.id],
  });
  const descriptors = [
    {suffix: 'user', subject: {type: 'user', id: user.id}},
    {suffix: 'role', subject: {type: 'role', id: role.id}},
    {suffix: 'team', subject: {type: 'team', id: team.id}},
  ];
  const assignments = [];
  for (const [index, descriptor] of descriptors.entries()) {
    const createdModel = await admin.createModel({
      displayName: `${descriptor.suffix} model`,
      sourceType: 'gateway',
      protocol: 'openai-compatible',
      endpoint: 'https://models.example.test/v1',
      upstreamModel: `upstream-${descriptor.suffix}`,
      credentialId: index === 0 ? modelCredential.id : null,
      capabilities: index === 0 ? ['text', 'streaming', 'reasoning', 'text'] : ['text', 'streaming', 'text'],
      ...(index === 0 ? {reasoningCompatibility: {
        thinkingFormat: 'deepseek', supportsReasoningEffort: true,
        requiresReasoningContentOnAssistantMessages: true,
      }} : {}),
      contextWindow: 32768,
      isDefault: index === 0,
      enabled: true,
    });
    descriptor.modelId = createdModel.id;
    assignments.push(await admin.createModelAssignment({modelId: descriptor.modelId, subject: descriptor.subject}));
  }

  const models = (await admin.listAdminModels()).models;
  assert(models.length === 3, 'Administrator model catalog did not contain three models');
  assert(models.filter(model => model.isDefault).length === 1, 'Enterprise catalog did not enforce one default model');
  assert(models.find(model => model.id === descriptors[0].modelId)?.reasoningCompatibility?.thinkingFormat === 'deepseek', 'Reasoning compatibility was not persisted');
  assert(models.filter(model => model.capabilities.includes('reasoning')).length === 1, 'Reasoning capability was not persisted');
  assert(models.every(model => model.capabilities.join(',') === (model.id === descriptors[0].modelId ? 'reasoning,streaming,text' : 'streaming,text')), 'Model capabilities were not normalized');
  await admin.updateModel(descriptors[0].modelId, {credentialId: null});
  assert((await admin.getModel(descriptors[0].modelId)).credentialId === null, 'Credential reference was not cleared');
  await admin.updateModel(descriptors[1].modelId, {isDefault: true});
  assert((await admin.listAdminModels()).models.filter(model => model.isDefault)[0].id === descriptors[1].modelId, 'Default model did not move atomically');

  await expectProblem(
    admin.createModelAssignment({modelId: descriptors[0].modelId, subject: descriptors[0].subject}),
    409,
    'ASSIGNMENT_EXISTS',
  );
  assert((await admin.listModelAssignments()).assignments.length === 3, 'Assignment list did not contain all three subject types');

  const agentStore = new MemoryTokenStore();
  const agent = new AepClient({baseUrl, tokenStore: agentStore});
  await agent.loginWithPassword({deploymentId: 'demo', username, password});
  await assertModelPricing(admin, agent, role.id, descriptors[0].modelId);
  const visible = (await agent.listModels()).models;
  assert(visible.length === 3, 'RBAC authorization union did not expose all models');
  assert(visible.every(model => !Object.hasOwn(model, 'credentialId')), 'Agent model catalog leaked credential metadata');
  assert(visible.some(model => model.reasoningCompatibility?.requiresReasoningContentOnAssistantMessages === true), 'Agent model catalog omitted reasoning replay compatibility');
  await admin.updateModel(descriptors[0].modelId, {reasoningCompatibility: null});
  assert(!Object.hasOwn(await admin.getModel(descriptors[0].modelId), 'reasoningCompatibility'), 'Reasoning compatibility was not cleared');
  await assertModelToken(agentStore, descriptors.map(item => item.modelId));

  await admin.deleteModelAssignment(assignments[2].id);
  assert((await agent.listModels()).models.length === 2, 'Team assignment revocation did not affect real-time discovery');
  assert(modelScopes(await agentStore.get()).length === 3, 'Existing model token changed without rotation');
  await agent.refreshSession();
  await assertModelToken(agentStore, descriptors.slice(0, 2).map(item => item.modelId));

  await admin.updateRole(role.id, {enabled: false});
  assert((await agent.listModels()).models.length === 1, 'Disabled role assignment remained discoverable');
  await admin.updateRole(role.id, {enabled: true});
  assert((await agent.listModels()).models.length === 2, 'Re-enabled role assignment was not restored');

  await admin.updateModel(descriptors[1].modelId, {enabled: false});
  assert((await agent.listModels()).models.length === 1, 'Disabled model remained discoverable');
  await agent.refreshSession();
  await assertModelToken(agentStore, descriptors.slice(0, 1).map(item => item.modelId));

  await admin.deleteModel(descriptors[1].modelId);
  await expectProblem(admin.getModelPricing(descriptors[1].modelId), 404, 'RESOURCE_NOT_FOUND');
  assert((await admin.listModelAssignments()).assignments.length === 1, 'Deleting a model did not cascade its assignment');
  assert((await agent.listModels()).models.length === 1, 'Deleted model remained discoverable');

  await admin.deleteTeam(team.id);
  await admin.deleteRole(role.id);

  const cliModels = await commandOutput('go', [
    'run', './cmd/aepctl', '--base-url', baseUrl, '--deployment', 'demo',
    '--username', 'admin', '--password', 'change-this-admin-password',
    'model', 'list',
  ]);
  assert(JSON.parse(cliModels).models.length === 2, 'aepctl model list did not return the administrator catalog');
}

async function assertDeploymentSettings(admin) {
  const initial = await admin.getDeploymentSettings();
  assert(initial.modelGatewayBaseUrl.override === null
    && initial.modelGatewayBaseUrl.effectiveValue === gatewayBaseUrl
    && initial.modelGatewayBaseUrl.source === 'env', 'Deployment settings did not resolve the environment gateway URL');

  const overrideUrl = 'https://runtime-gateway.example.com/v1';
  const updated = await admin.updateDeploymentSettings({modelGatewayBaseUrl: overrideUrl});
  assert(updated.modelGatewayBaseUrl.override === overrideUrl
    && updated.modelGatewayBaseUrl.effectiveValue === overrideUrl
    && updated.modelGatewayBaseUrl.source === 'override', 'Deployment settings update did not store the runtime override');
  assert((await admin.getMetadata()).modelGateway?.baseUrl === overrideUrl, 'Metadata did not advertise the runtime gateway override');

  await expectProblem(
    admin.updateDeploymentSettings({modelGatewayBaseUrl: 'http://higress.svc.cluster.local/v1'}),
    422,
    'INVALID_DEPLOYMENT_SETTINGS',
  );

  const cleared = await admin.updateDeploymentSettings({modelGatewayBaseUrl: null});
  assert(cleared.modelGatewayBaseUrl.override === null
    && cleared.modelGatewayBaseUrl.effectiveValue === gatewayBaseUrl
    && cleared.modelGatewayBaseUrl.source === 'env', 'Clearing the runtime override did not restore the environment value');
  assert((await admin.getMetadata()).modelGateway?.baseUrl === gatewayBaseUrl, 'Metadata did not fall back to the environment gateway URL');
}

async function assertModelToken(store, expectedScopes) {
  const tokens = await store.get();
  assert(tokens?.modelAccessToken, 'Session did not include a model access token');
  const header = decodeJwtPart(tokens.modelAccessToken, 0);
  const claims = decodeJwtPart(tokens.modelAccessToken, 1);
  assert(claims.token_use === 'model', 'model JWT token_use is invalid');
  assert(claims.deployment_id === 'demo' && claims.sub, 'model JWT identity claims are invalid');
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

async function assertModelPricing(admin, user, roleId, modelId) {
  const pricing = {currency: 'CNY', inputPricePerMillionTokens: '0.000001',
    outputPricePerMillionTokens: '0', cachedInputPricePerMillionTokens: '0.01', source: 'Internal mock reference'};
  const unset = await admin.getModelPricing(modelId);
  assert(unset.version === 0 && unset.pricing === null && unset.updatedAt === null, 'Unset prices were invented');
  await expectProblem(user.getModelPricing(modelId), 403, 'ACCESS_DENIED');
  await expectProblem(user.putModelPricing(modelId, {pricing, expectedVersion: 0}), 403, 'ACCESS_DENIED');
  const saved = await admin.putModelPricing(modelId, {pricing, expectedVersion: 0});
  assert(saved.version === 1 && saved.pricing.inputPricePerMillionTokens === '0.000001'
    && saved.pricing.outputPricePerMillionTokens === '0' && saved.updatedAt, 'Exact reference prices were not saved');
  await admin.updateRole(roleId, {permissions: ['models.read']});
  assert((await user.getModelPricing(modelId)).version === 1, 'Read-only price access denied');
  await expectProblem(user.putModelPricing(modelId, {pricing: null, expectedVersion: 1}), 403, 'ACCESS_DENIED');
  await admin.updateRole(roleId, {permissions: []});
  assert(!(await user.listModels()).models.some(m => Object.hasOwn(m, 'pricing')), 'Prices leaked to runtime model descriptors');
  const writes = await Promise.allSettled([
    admin.putModelPricing(modelId, {pricing: {...pricing, currency: 'USD'}, expectedVersion: 1}),
    admin.putModelPricing(modelId, {pricing: {...pricing, inputPricePerMillionTokens: '2.5'}, expectedVersion: 1}),
  ]);
  assert(writes.filter(r => r.status === 'fulfilled').length === 1, 'Concurrent prices overwrote each other');
  const conflict = writes.find(r => r.status === 'rejected').reason;
  assert(conflict.status === 409 && conflict.code === 'MODEL_PRICING_VERSION_CONFLICT', 'Version conflict missing');
  const beforeRestart = await admin.getModelPricing(modelId);
  await command('docker', ['compose', '-p', project, '-f', composeFile, 'restart', 'control-service'], composeEnv);
  await waitForHealth();
  assert(JSON.stringify(await admin.getModelPricing(modelId)) === JSON.stringify(beforeRestart), 'Prices lost after service restart');
  const cleared = await admin.putModelPricing(modelId, {pricing: null, expectedVersion: 2});
  assert(cleared.version === 3 && cleared.pricing === null, 'Clearing reset the concurrency version');
  await expectProblem(admin.putModelPricing(modelId, {pricing, expectedVersion: 0}), 409, 'MODEL_PRICING_VERSION_CONFLICT');
  await expectProblem(admin.putModelPricing(modelId, {pricing: {...pricing, inputPricePerMillionTokens: '-1'}, expectedVersion: 3}), 400, 'INVALID_MODEL_PRICING');
  await commandOutput('docker', ['compose', '-p', project, '-f', composeFile, 'exec', '-T', 'postgres', 'psql', '-U', 'aep', '-d', 'aep', '-v', 'ON_ERROR_STOP=1', '-c',
    "INSERT INTO deployments(id,name) VALUES('pricing-isolation','Pricing isolation'); INSERT INTO models(deployment_id,id,display_name,source_type,protocol) VALUES('pricing-isolation','pricing-foreign','Foreign price model','gateway','openai-compatible')"], composeEnv);
  await expectProblem(admin.getModelPricing('pricing-foreign'), 404, 'RESOURCE_NOT_FOUND');
  await expectProblem(admin.putModelPricing('pricing-foreign', {pricing, expectedVersion: 0}), 404, 'RESOURCE_NOT_FOUND');
  await admin.putModelPricing(modelId, {pricing, expectedVersion: 3});
  console.log('Model pricing: persistence, restart, exact decimals, RBAC, deployment isolation, concurrent updates and clear passed.');
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

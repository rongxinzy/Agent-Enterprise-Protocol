// Opt-in acceptance against a running LOCAL AEP service and credential publisher.
// Uses disposable mock keys; never invokes a real model provider.
import assert from 'node:assert/strict';
import {spawnSync} from 'node:child_process';
import {randomUUID} from 'node:crypto';
import {setTimeout as delay} from 'node:timers/promises';

const required = name => {
  const value = process.env[name];
  assert(value, `Missing ${name}`);
  return value;
};
const base = new URL(required('ZHIYUAN_AEP_BASE_URL'));
assert(['localhost', '127.0.0.1', '[::1]'].includes(base.hostname), 'Acceptance must target a local service');
const context = required('ZHIYUAN_AEP_KUBE_CONTEXT');
assert(context.startsWith('kind-'), 'Acceptance requires an explicit Kind context');
const kubeconfig = required('KUBECONFIG');
const deploymentId = required('ZHIYUAN_AEP_DEPLOYMENT_ID');
const username = required('ZHIYUAN_AEP_ADMIN_USERNAME');
const password = required('ZHIYUAN_AEP_ADMIN_PASSWORD');
const timeoutMs = 60_000;
const modelIds = [];
const credentialIds = [];
let accessToken, refreshToken;

function kubectl(args) {
  const result = spawnSync('kubectl', ['--kubeconfig', kubeconfig, '--context', context, ...args], {encoding: 'utf8', windowsHide: true, timeout: 15_000});
  // Do not expose Kubernetes stderr: a failed write can echo object values.
  assert.equal(result.status, 0, 'Acceptance Kubernetes command failed');
  return result.stdout;
}

function secret(id) {
  const result = kubectl(['-n', 'aep-credentials', 'get', 'secret', `aep-credential-${id}`, '--ignore-not-found', '--show-managed-fields', '-o', 'json']);
  return result.trim() ? JSON.parse(result) : null;
}

async function api(path, method = 'GET', body) {
  const response = await fetch(new URL(`/aep/v1/${path}`, base), {
    method,
    headers: {'X-AEP-Protocol-Version': '1.0', 'Content-Type': 'application/json', ...(accessToken ? {Authorization: `Bearer ${accessToken}`} : {})},
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(15_000),
  });
  assert(response.ok, `Acceptance API ${method} ${path.split('?')[0]} failed (${response.status})`);
  return response.status === 204 ? null : response.json();
}

async function until(condition, message) {
  const deadline = Date.now() + timeoutMs;
  do {
    if (await condition()) return;
    await delay(1000);
  } while (Date.now() < deadline);
  throw Error(message);
}

const mockKey = 'disposable-credential-publisher-fixture';
const rotatedKey = 'disposable-credential-publisher-rotated';
const keyEquals = (id, key) => {
  const value = secret(id);
  return value && Buffer.from(value.data?.['api-key'] ?? '', 'base64').toString() === key;
};

async function createCredential(mode = 'server_only') {
  const result = await api('admin/credentials', 'POST', {name: `publisher-${randomUUID()}`, service: 'mock-provider', type: 'api_key', deliveryMode: mode, value: mockKey, enabled: true});
  credentialIds.push(result.id);
  assert(!('value' in result) && !JSON.stringify(result).includes(mockKey), 'Credential response exposed plaintext');
  return result.id;
}

async function createModel(credentialId) {
  const result = await api('admin/models', 'POST', {displayName: `publisher-${randomUUID()}`, sourceType: 'gateway', protocol: 'openai-compatible', endpoint: 'http://mock-openai.aep-system.svc.cluster.local:8080/v1', upstreamModel: 'mock-upstream', credentialId, capabilities: ['text'], isDefault: false, enabled: true});
  modelIds.push(result.id);
  return result.id;
}

try {
  const auth = await api('auth/password/login', 'POST', {deploymentId, username, password});
  accessToken = auth.accessToken;
  refreshToken = auth.refreshToken;
  const id = await createCredential();
  assert.equal(secret(id), null, 'Unreferenced Credential should not be delivered');
  const modelA = await createModel(id);
  await until(() => keyEquals(id, mockKey), 'Console/API Credential was not automatically delivered');
  const delivered = secret(id);
  assert.equal(delivered.metadata.labels['app.kubernetes.io/managed-by'], 'aep-credential-publisher');
  assert.equal(delivered.metadata.annotations['credentials.aep.zhiyuan.io/deployment-id'], deploymentId);
  assert(!delivered.metadata.ownerReferences?.length, 'Direct delivery unexpectedly depends on ExternalSecret');
  assert(delivered.metadata.managedFields.some(field => field.manager === 'aep-credential-publisher'), 'Target was not written by the publisher');
  console.log('PASS management API → sealed DB → publisher-owned Kubernetes Secret');

  await api(`admin/credentials/${id}/rotate`, 'POST', {value: rotatedKey});
  await until(() => keyEquals(id, rotatedKey), 'Credential rotation was not delivered');
  console.log('PASS credential rotation');

  await api(`admin/credentials/${id}`, 'PATCH', {enabled: false});
  await until(() => secret(id) === null, 'Disabled Credential retained its owned Secret');
  await api(`admin/credentials/${id}`, 'PATCH', {enabled: true});
  await until(() => keyEquals(id, rotatedKey), 'Re-enabled Credential was not restored');
  console.log('PASS disable and re-enable');

  const modelB = await createModel(id);
  await api(`admin/models/${modelA}`, 'PATCH', {enabled: false});
  await delay(6500);
  assert(keyEquals(id, rotatedKey), 'Shared Credential was removed while still in use');
  await api(`admin/models/${modelB}`, 'PATCH', {enabled: false});
  await until(() => secret(id) === null, 'Last disabled model did not withdraw its Secret');
  await api(`admin/models/${modelB}`, 'PATCH', {enabled: true});
  await until(() => keyEquals(id, rotatedKey), 'Model re-enable did not restore Secret');
  console.log('PASS shared references and last-model withdrawal');

  const agentId = await createCredential('client');
  await createModel(agentId);
  await delay(6500);
  assert.equal(secret(agentId), null, 'Agent-delivered Credential entered the gateway namespace');
  await api(`admin/credentials/${id}`, 'PATCH', {deliveryMode: 'client'});
  await until(() => secret(id) === null, 'Delivery-mode change did not withdraw Secret');
  await api(`admin/credentials/${id}`, 'PATCH', {deliveryMode: 'server_only'});
  await until(() => keyEquals(id, rotatedKey), 'Restoring server-only delivery failed');
  console.log('PASS Agent delivery exclusion and mode transitions');
} finally {
  let cleanupFailed = false;
  for (const id of modelIds.reverse()) {
    try { await api(`admin/models/${id}`, 'DELETE'); } catch { cleanupFailed = true; }
  }
  for (const id of credentialIds) {
    try {
      await api(`admin/credentials/${id}`, 'DELETE');
      await until(() => secret(id) === null, 'Deleted Credential retained its owned Secret');
    } catch { cleanupFailed = true; }
  }
  if (refreshToken) {
    try { await api('auth/logout', 'POST', {refreshToken}); } catch { cleanupFailed = true; }
  }
  assert(!cleanupFailed, 'Acceptance cleanup failed; inspect temporary publisher fixtures');
  console.log('PASS temporary model/Credential/Secret cleanup and session logout');
}

import {spawn} from 'node:child_process';
import * as nodeCrypto from 'node:crypto';
import {mkdtemp, rm} from 'node:fs/promises';
import {createServer} from 'node:http';
import {tmpdir} from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

import {AepClient, MemoryTokenStore} from '../../packages/aep-sdk-node/dist/index.js';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const composeFile = path.join(root, 'deploy', 'compose', 'compose.yaml');
const overlayFile = path.join(root, 'tests', 'e2e', 'm3-data-plane.compose.yaml');
const project = 'aep-m3-data-plane-e2e';
const port = process.env.AEP_M3_DATA_PLANE_E2E_PORT ?? '18088';
const kubePort = process.env.AEP_M3_KUBERNETES_E2E_PORT ?? '18089';
const baseUrl = `http://localhost:${port}`;
const kubeUrl = `http://127.0.0.1:${kubePort}`;
const reconcilerHealthUrl = 'http://127.0.0.1:18091';
const composeEnv = {AEP_PORT: port, AEP_MINIO_CONSOLE_PORT: process.env.AEP_M3_MINIO_CONSOLE_PORT ?? '19008'};
const outputDir = await mkdtemp(path.join(tmpdir(), 'aep-m3-reconciler-'));
const reconcilerBinary = path.join(outputDir, process.platform === 'win32' ? 'aep-gateway-reconciler.exe' : 'aep-gateway-reconciler');
const resources = new Map();
// Fixture Secret store the mock Kubernetes API serves to the reconciler's
// credentialRef reads (KubernetesApplier.ReadSecret GETs these).
const kubeSecrets = new Map([
  ['provider-secrets', {'api-key-a': 'provider-secret-value-a', 'api-key-c': 'provider-secret-value-c', 'api-key-d': 'provider-secret-value-d'}],
  ['provider-secrets-v2', {'api-key-b': 'provider-secret-value-b'}],
]);
let kubeAvailable = true;
let failWasm = false;
let applyCount = 0;
let reconciler;
let reconcilerErrors = '';

const kubeServer = createServer(async (request, response) => {
  const chunks = [];
  for await (const chunk of request) chunks.push(Buffer.from(chunk));
  const body = Buffer.concat(chunks).toString('utf8');
  if (!kubeAvailable) {
    response.writeHead(503, {'content-type': 'application/json'}).end('{"message":"Kubernetes unavailable"}');
    return;
  }
  if (failWasm && request.url.includes('/wasmplugins/')) {
    response.writeHead(422, {'content-type': 'application/json'}).end('{"message":"WasmPlugin admission rejected"}');
    return;
  }
  const target = new URL(request.url, kubeUrl);
  // Minimal Lease semantics for leader election: the lease never exists on
  // GET (so the elector keeps re-acquiring), creates/renews succeed.
  if (target.pathname.startsWith('/apis/coordination.k8s.io/')) {
    if (request.method === 'GET') {
      response.writeHead(404, {'content-type': 'application/json'}).end('{"message":"Lease not found"}');
      return;
    }
    response.writeHead(request.method === 'POST' ? 201 : 200, {'content-type': 'application/json'}).end(body || '{}');
    return;
  }
  if (request.method === 'GET') {
    const match = /^\/api\/v1\/namespaces\/higress-system\/secrets\/([^/]+)$/.exec(target.pathname);
    assert(match, `unexpected Kubernetes GET path ${target.pathname}`);
    assert(request.headers.authorization === 'Bearer m3-kubernetes-service-account', 'Kubernetes bearer token was missing on the Secret read');
    const data = kubeSecrets.get(match[1]);
    if (!data) {
      response.writeHead(404, {'content-type': 'application/json'}).end('{"message":"Secret not found"}');
      return;
    }
    const encoded = Object.fromEntries(Object.entries(data).map(([key, value]) => [key, Buffer.from(value, 'utf8').toString('base64')]));
    response.writeHead(200, {'content-type': 'application/json'}).end(JSON.stringify({data: encoded}));
    return;
  }
  if (request.method === 'DELETE') {
    resources.delete(target.pathname);
    applyCount++;
    response.writeHead(200, {'content-type': 'application/json'}).end('{"status":"Success"}');
    return;
  }
  assert(request.method === 'PATCH', `Kubernetes method was ${request.method}`);
  assert(request.headers.authorization === 'Bearer m3-kubernetes-service-account', 'Kubernetes bearer token was missing');
  assert(request.headers['content-type'] === 'application/apply-patch+yaml', 'server-side apply content type was missing');
  assert(target.searchParams.get('fieldManager') === 'aep-gateway-reconciler', 'field manager was incorrect');
  assert(target.searchParams.get('force') === 'true', 'force ownership was not enabled');
  resources.set(target.pathname, body);
  applyCount++;
  response.writeHead(201, {'content-type': 'application/json'}).end('{"metadata":{"name":"applied"}}');
});

try {
  await listen(kubeServer, Number(kubePort));
  await compose('up', '-d', '--build');
  await waitFor(async () => assert((await fetch(`${baseUrl}/readyz`)).ok, 'control service is not ready'));
  await command('go', ['build', '-o', reconcilerBinary, './services/gateway-reconciler/cmd/server']);
  const admin = new AepClient({baseUrl, tokenStore: new MemoryTokenStore()});
  await admin.loginWithPassword({deploymentId: 'demo', username: 'admin', password: 'change-this-admin-password'});
  startReconciler();
  await waitForHealth('/livez', 200);

  const first = await admin.putDataPlaneDesiredState({revision: 'rev-1', routes: [route('chat', '/v1/chat', 'provider-a', 'api-key-a', 'provider-secrets', 'deepseek')]});
  await waitForReady(admin, 'rev-1');
  await waitForHealth('/readyz', 200);
  assert(resources.size === 2, 'Ingress and WasmPlugin were not both applied');
  const firstCount = applyCount;
  const firstResources = snapshot();
  assert(firstResources.includes("type: 'deepseek'"), 'DeepSeek provider type was not rendered');
  assert(firstResources.includes("apiTokens:\n            - 'provider-secret-value-a'"), 'resolved Secret value was not inlined as an ai-proxy apiToken');
  assert(!firstResources.includes('credentialRef'), 'credentialRef leaked into the rendered Higress resources');
  assert(!JSON.stringify(first).includes('provider-secret-value-a'), 'Secret value leaked into the desired-state response');
  assert(!JSON.stringify(await admin.getDataPlaneStatus()).includes('provider-secret-value-a'), 'Secret value leaked into the data-plane status');

  const repeated = await admin.putDataPlaneDesiredState({revision: 'rev-1', routes: [route('chat', '/v1/chat', 'provider-a', 'api-key-a', 'provider-secrets', 'deepseek')]});
  assert(repeated.contentHash === first.contentHash, 'same revision was not idempotent');
  await waitFor(() => assert(applyCount >= firstCount + 2, 'periodic convergence did not reapply desired state'));
  assert(snapshot() === firstResources, 'idempotent reconciliation changed resources');

  await admin.putDataPlaneDesiredState({revision: 'rev-2', routes: [route('chat', '/v1/responses', 'provider-b', 'api-key-b', 'provider-secrets-v2')]});
  await waitForReady(admin, 'rev-2');
  assert(snapshot().includes('/v1/responses'), 'route update was not applied');
  assert(snapshot().includes("apiTokens:\n            - 'provider-secret-value-b'"), 'rotated Secret value was not inlined as an ai-proxy apiToken');
  assert(!snapshot().includes('provider-secret-value-a'), 'previous Secret value remained after Secret rotation');
  assert(!snapshot().includes('credentialRef') && !snapshot().includes('provider-secrets-v2'), 'Secret reference leaked into the rendered Higress resources');

  for (const key of resources.keys()) resources.set(key, 'drifted-by-operator');
  await waitFor(() => assert(!snapshot().includes('drifted-by-operator'), 'drift was not corrected'));

  failWasm = true;
  await admin.putDataPlaneDesiredState({revision: 'rev-3', routes: [route('chat', '/v1/chat', 'provider-c', 'api-key-c')]});
  await waitForStatus(admin, status => status.state === 'error' && status.errorCode === 'KUBERNETES_APPLY_FAILED');
  await waitForHealth('/readyz', 503);
  await waitForHealth('/livez', 200);
  failWasm = false;
  await waitForReady(admin, 'rev-3');
  await waitForHealth('/readyz', 200);

  kubeAvailable = false;
  await admin.putDataPlaneDesiredState({revision: 'rev-4', routes: [route('chat', '/v1/chat', 'provider-d', 'api-key-d')]});
  await waitForStatus(admin, status => status.state === 'error' && status.errorCode === 'KUBERNETES_APPLY_FAILED');
  await waitForHealth('/readyz', 503);
  kubeAvailable = true;
  await waitForReady(admin, 'rev-4');
  await waitForHealth('/readyz', 200);

  await expectProblem(admin.putDataPlaneDesiredState({revision: 'malformed', routes: [{...route('bad', '', 'provider', 'key')}]}), 400, 'INVALID_DATA_PLANE_STATE');
  await expectProblem(admin.putDataPlaneDesiredState({revision: 'unsupported-provider', routes: [{...route('bad-provider', '/v1/chat', 'provider', 'key'), providerType: 'unknown'}]}), 400, 'INVALID_DATA_PLANE_STATE');

  // Anthropic routes: absolute endpoints only, provider types forbidden, and
  // two models whose sanitized path prefixes collide fail at render time.
  const anthropicRoute = (modelId, endpoint = 'https://open.bigmodel.cn/api/anthropic') =>
    ({modelId, enabled: true, endpoint, upstreamModel: 'glm-5.3-flash', protocol: 'anthropic', credentialRef: {name: 'provider-secrets', key: 'api-key-a', namespace: 'higress-system'}});
  await expectProblem(admin.putDataPlaneDesiredState({revision: 'anthropic-relative', routes: [anthropicRoute('bench-anthropic', '/v1')]}), 400, 'INVALID_DATA_PLANE_STATE');
  await expectProblem(admin.putDataPlaneDesiredState({revision: 'anthropic-provider', routes: [{...anthropicRoute('bench-anthropic'), providerType: 'openai'}]}), 400, 'INVALID_DATA_PLANE_STATE');
  await admin.putDataPlaneDesiredState({revision: 'anthropic-collision', routes: [anthropicRoute('Bench GLM'), anthropicRoute('bench-glm', 'https://api.anthropic.com')]});
  await waitForStatus(admin, status => status.state === 'error' && status.errorCode === 'RENDER_FAILED');
  await waitForHealth('/readyz', 503);
  await admin.putDataPlaneDesiredState({revision: 'rev-4', routes: [route('chat', '/v1/chat', 'provider-d', 'api-key-d')]});
  await waitForReady(admin, 'rev-4');
  await waitForHealth('/readyz', 200);

  await compose('restart', 'control-service');
  await waitFor(async () => assert((await fetch(`${baseUrl}/readyz`)).ok, 'control service did not recover'));
  await waitForReady(admin, 'rev-4');

  await stopReconciler();
  const beforeRestart = applyCount;
  startReconciler();
  await waitFor(() => assert(applyCount >= beforeRestart + 2, 'reconciler restart did not converge'));

  await admin.putDataPlaneDesiredState({revision: 'rev-5', routes: [{...route('chat', '/v1/chat', 'provider-d', 'api-key-d'), enabled: false}]});
  await waitForReady(admin, 'rev-5');
  assert(!snapshot().includes("path: '/v1/chat'"), 'disabled route remained in Ingress');

  // Catalog-derived publication: the model catalog is the route source of truth.
  const credential = await admin.createCredential({name: 'Provider catalog key', service: 'provider-catalog', type: 'api_key', deliveryMode: 'server_only', value: 'catalog-secret-value-a', enabled: true});
  kubeSecrets.set(`aep-credential-${credential.id}`, {'api-key': 'catalog-secret-value-a'});
  await admin.createModel({displayName: 'Catalog Chat', sourceType: 'gateway', protocol: 'openai-compatible', endpoint: 'http://provider-catalog/v1', upstreamModel: 'provider-catalog-chat', credentialId: credential.id, capabilities: ['text'], isDefault: false, enabled: true});
  await admin.createModel({displayName: 'Catalog Disabled', sourceType: 'gateway', protocol: 'openai-compatible', endpoint: 'http://provider-catalog/v1', upstreamModel: 'provider-catalog-disabled', capabilities: ['text'], isDefault: false, enabled: false});
  await admin.createModel({displayName: 'Catalog Local', sourceType: 'local', protocol: 'openai-compatible', localModelRef: 'local-gguf', capabilities: ['text'], isDefault: false, enabled: true});

  const published = await admin.publishDataPlaneRoutes();
  assert(published.revision.startsWith('catalog-'), 'publish did not assign a catalog revision');
  assert(published.routes.length === 2 && published.routes.some(candidate => candidate.modelId === 'catalog-chat' && candidate.enabled), 'publish derived the wrong route set');
  const disabledDerived = published.routes.find(candidate => candidate.modelId === 'catalog-disabled');
  assert(disabledDerived && disabledDerived.enabled === false, 'disabled catalog model must ride along as an enabled=false route');
  const catalogRoute = published.routes.find(candidate => candidate.modelId === 'catalog-chat');
  assert(catalogRoute.credentialRef?.name === `aep-credential-${credential.id}` && catalogRoute.credentialRef?.key === 'api-key', 'publish did not map the Credential to the conventional Secret');
  assert(!JSON.stringify(published).includes('catalog-secret-value-a'), 'Secret value leaked into the publish response');
  await waitForReady(admin, published.revision);
  assert(snapshot().includes("'catalog-chat': 'provider-catalog-chat'"), 'catalog modelMapping was not rendered');
  assert(snapshot().includes("apiTokens:\n            - 'catalog-secret-value-a'"), 'derived credentialRef was not resolved and inlined');
  assert(!snapshot().includes('catalog-disabled') && !snapshot().includes('catalog-local'), 'non-publishable catalog models leaked into the gateway');
  let comparison = (await admin.getDataPlaneStatus()).catalogComparison;
  assert(comparison && comparison.missing.length === 0 && comparison.extra.length === 0 && comparison.mismatched.length === 0, `published catalog still drifted: ${JSON.stringify(comparison)}`);
  assert(!JSON.stringify(await admin.getDataPlaneStatus()).includes('catalog-secret-value-a'), 'Secret value leaked into the data-plane status comparison');

  const republished = await admin.publishDataPlaneRoutes();
  assert(republished.revision === published.revision && republished.contentHash === published.contentHash, 'republishing an unchanged catalog was not idempotent');

  await admin.putDataPlaneDesiredState({revision: 'rev-drift', routes: [route('ghost', '/v1/ghost', 'provider-ghost', 'api-key-a')]});
  comparison = (await admin.getDataPlaneStatus()).catalogComparison;
  assert(comparison.extra.includes('ghost') && comparison.missing.includes('catalog-chat'), `manual drift was not reported: ${JSON.stringify(comparison)}`);
  const corrected = await admin.publishDataPlaneRoutes();
  assert(corrected.revision === published.revision, 'republishing the catalog did not restore the derived state');
  await waitForReady(admin, published.revision);
  assert(!snapshot().includes('ghost'), 'ghost route survived catalog publication');

  // Anthropic catalog models publish into a self-contained EnvoyFilter
  // passthrough under a per-model path prefix — no ai-proxy involvement.
  const anthropicCredential = await admin.createCredential({name: 'BigModel key', service: 'bigmodel', type: 'api_key', deliveryMode: 'server_only', value: 'anthropic-secret-value', enabled: true});
  kubeSecrets.set(`aep-credential-${anthropicCredential.id}`, {'api-key': 'anthropic-secret-value'});
  await admin.createModel({displayName: 'Bench Anthropic', sourceType: 'gateway', protocol: 'anthropic', endpoint: 'https://open.bigmodel.cn/api/anthropic', upstreamModel: 'glm-5.3-flash', credentialId: anthropicCredential.id, capabilities: ['text'], isDefault: false, enabled: true});
  const anthropicPublished = await admin.publishDataPlaneRoutes();
  const derivedAnthropicRoute = anthropicPublished.routes.find(candidate => candidate.modelId === 'bench-anthropic');
  assert(derivedAnthropicRoute?.protocol === 'anthropic' && !derivedAnthropicRoute.providerType, `anthropic derived route = ${JSON.stringify(derivedAnthropicRoute)}`);
  await waitForReady(admin, anthropicPublished.revision);
  assert(resources.size === 4, `expected openai ingress + anthropic ingress + envoyfilter + wasmplugin, got ${[...resources.keys()]}`);
  const anthropicName = `aep-anthropic-${suffixOf('bench-anthropic')}`;
  const filter = resources.get(`/apis/networking.istio.io/v1alpha3/namespaces/higress-system/envoyfilters/${anthropicName}`);
  const anthropicIngress = resources.get(`/apis/networking.k8s.io/v1/namespaces/higress-system/ingresses/${anthropicName}`);
  assert(filter && anthropicIngress, 'anthropic resources were not applied under the model-derived names');
  assert(anthropicIngress.includes("path: '/bench-anthropic'"), 'anthropic ingress lost the per-model path prefix');
  for (const expected of [
    'applyTo: CLUSTER',
    "address: 'open.bigmodel.cn'",
    "port_value: 443",
    "sni: 'open.bigmodel.cn'",
    `cluster: '${anthropicName}'`,
    "host_rewrite_literal: 'open.bigmodel.cn'",
    "regex: '^/bench-anthropic/(.*)$'",
    "substitution: '/api/anthropic/\\1'",
    "value: 'anthropic-secret-value'",
    "value: 'Bearer anthropic-secret-value'",
  ]) {
    assert(filter.includes(expected), `envoyfilter missing ${expected}:\n${filter}`);
  }
  assert(!filter.includes('credentialRef') && !filter.includes('aep-credential'), 'credential reference leaked into the envoyfilter');
  comparison = (await admin.getDataPlaneStatus()).catalogComparison;
  assert(comparison.missing.length === 0 && comparison.extra.length === 0 && comparison.mismatched.length === 0, `anthropic catalog drifted: ${JSON.stringify(comparison)}`);

  // Disabling the anthropic model removes its whole resource pair.
  await admin.updateModel('bench-anthropic', {enabled: false});
  const disabledPublish = await admin.publishDataPlaneRoutes();
  await waitForReady(admin, disabledPublish.revision);
  assert(!snapshot().includes(anthropicName), 'disabled anthropic model left resources behind');
  assert(snapshot().includes("'catalog-chat': 'provider-catalog-chat'"), 'openai catalog route was disturbed by the anthropic leg');
  console.log('AEP M3 live data-plane automation scenario passed.');
} catch (error) {
  if (reconcilerErrors) console.error(reconcilerErrors);
  await compose('logs', '--no-color', '--tail=300', true);
  throw error;
} finally {
  await stopReconciler();
  await new Promise(resolve => kubeServer.close(resolve));
  await compose('down', '-v', '--remove-orphans', true);
  await rm(outputDir, {recursive: true, force: true});
}

function route(modelId, endpoint, upstreamModel, key, name = 'provider-secrets', providerType = 'openai') {
  return {modelId, enabled: true, endpoint, upstreamModel, protocol: 'openai-compatible', providerType, credentialRef: {name, key, namespace: 'higress-system'}};
}

// Mirrors resourceSuffix in the reconciler so the test can predict resource
// names: lowercase [a-z0-9-], cap 40, then - plus 8 hex of sha256(modelID).
function suffixOf(value) {
  const clean = value.toLowerCase().replace(/[^a-z0-9-]/g, '-').replace(/^-+|-+$/g, '').slice(0, 40).replace(/^-+|-+$/g, '');
  const digest = nodeCrypto.createHash('sha256').update(value).digest('hex').slice(0, 8);
  return `${clean || 'tenant'}-${digest}`;
}

function snapshot() {
  return [...resources.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([key, value]) => `${key}\n${value}`).join('\n');
}

function startReconciler() {
  reconciler = spawn(reconcilerBinary, [], {
    cwd: root,
    env: {...process.env,
      AEP_RECONCILER_CONTROL_URL: baseUrl,
      AEP_DATA_PLANE_RECONCILER_TOKEN: 'm3-e2e-reconciler-token',
      AEP_RECONCILER_TENANTS: 'demo',
      AEP_RECONCILER_OUTPUT_DIR: outputDir,
      AEP_RECONCILER_INTERVAL: '500ms',
      AEP_RECONCILER_ADDRESS: '127.0.0.1:18091',
      AEP_RECONCILER_KUBERNETES_URL: kubeUrl,
      AEP_RECONCILER_KUBERNETES_TOKEN: 'm3-kubernetes-service-account',
      AEP_RECONCILER_KUBERNETES_CA_FILE: '',
    },
    stdio: ['ignore', 'ignore', 'pipe'],
    shell: false,
  });
  reconciler.stderr.on('data', chunk => { reconcilerErrors += chunk.toString('utf8'); });
}

async function stopReconciler() {
  if (!reconciler) return;
  const child = reconciler;
  reconciler = undefined;
  if (child.exitCode !== null) return;
  child.kill();
  await new Promise(resolve => {
    const timer = setTimeout(resolve, 5_000);
    child.once('exit', () => { clearTimeout(timer); resolve(); });
  });
}

async function waitForReady(admin, revision) {
  await waitForStatus(admin, status => status.state === 'ready' && status.observedRevision === revision);
}

async function waitForStatus(admin, predicate) {
  await waitFor(async () => {
    const status = await admin.getDataPlaneStatus();
    assert(predicate(status), `data-plane status was ${JSON.stringify(status)}`);
  });
}

function waitForHealth(pathname, status) {
  return waitFor(async () => {
    const response = await fetch(reconcilerHealthUrl + pathname);
    assert(response.status === status, `${pathname} returned ${response.status}, expected ${status}`);
  });
}

async function waitFor(operation) {
  const deadline = Date.now() + 120_000;
  let lastError;
  while (Date.now() < deadline) {
    try { await operation(); return; } catch (error) { lastError = error; }
    await new Promise(resolve => setTimeout(resolve, 500));
  }
  throw new Error(`condition was not met: ${lastError?.message ?? 'unknown error'}`);
}

async function expectProblem(promise, status, code) {
  try { await promise; } catch (error) {
    assert(error.status === status && error.code === code, `expected ${status} ${code}, received ${error.status} ${error.code}`);
    return;
  }
  throw new Error(`expected ${status} ${code}`);
}

function listen(server, listenPort) {
  return new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(listenPort, '127.0.0.1', resolve);
  });
}

function compose(...args) {
  let allowFailure = false;
  if (args.at(-1) === true) { allowFailure = true; args.pop(); }
  return command('docker', ['compose', '-p', project, '-f', composeFile, '-f', overlayFile, ...args], composeEnv, allowFailure);
}

function command(executable, args, extraEnv = {}, allowFailure = false) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {cwd: root, env: {...process.env, ...extraEnv}, stdio: 'inherit', shell: false});
    child.on('error', reject);
    child.on('exit', code => code === 0 || allowFailure ? resolve() : reject(new Error(`${executable} ${args.join(' ')} exited with ${code}`)));
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

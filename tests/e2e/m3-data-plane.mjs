// Live data-plane E2E: a real Compose stack plus the reconciler binary drive the
// desired-state API against a mock Kubernetes API.
//
// PUT /aep/v1/admin/data-plane/desired-state is an escape hatch on top of the
// model catalog, not a way to invent providers: every accepted route must carry
// the endpoint the catalog holds for the model and the credential Secret the
// catalog derives from its binding (aep-credential-<credentialId>, key api-key),
// so a route can neither read an arbitrary Secret nor send a credential to an
// arbitrary upstream. Only rendering fields stay overridable (providerType,
// upstreamModel, protocol, enabled) - that is what lets a route ask for Higress'
// native deepseek provider type. Repointing a route at another endpoint or
// credential means updating the catalog first (createModel / updateModel), so
// every fixture below prepares the catalog and then mirrors it into routes.

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
// credentialRef reads (KubernetesApplier.ReadSecret GETs these). Entries are
// keyed by the conventional Secret name the catalog derives for a Credential
// (aep-credential-<credentialId>) and are seeded by useCredential.
const kubeSecrets = new Map();
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
    const match = /^\/api\/v1\/namespaces\/aep-credentials\/secrets\/([^/]+)$/.exec(target.pathname);
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

  // The escape hatch renders catalog routes, so the model and its credential
  // binding come first: routes below are that catalog entry plus overrides.
  const credentialA = await useCredential(admin, 'Provider A key', 'provider-a', 'provider-secret-value-a');
  const chatModel = await useModel(admin, {displayName: 'Chat', endpoint: '/v1/chat', upstreamModel: 'provider-a', credentialId: credentialA.id});

  const first = await admin.putDataPlaneDesiredState({revision: 'rev-1', routes: [routeFor(chatModel, {providerType: 'deepseek'})]});
  await waitForReady(admin, 'rev-1');
  await waitForHealth('/readyz', 200);
  assert(resources.size === 3, 'Ingress, ai-proxy and ai-statistics were not all applied');
  const firstCount = applyCount;
  const firstResources = snapshot();
  assert(firstResources.includes("type: 'deepseek'"), 'DeepSeek provider type was not rendered');
  assert(firstResources.includes("apiTokens:\n            - 'provider-secret-value-a'"), 'resolved Secret value was not inlined as an ai-proxy apiToken');
  assert(!firstResources.includes('credentialRef'), 'credentialRef leaked into the rendered Higress resources');
  assert(!JSON.stringify(first).includes('provider-secret-value-a'), 'Secret value leaked into the desired-state response');
  assert(!JSON.stringify(await admin.getDataPlaneStatus()).includes('provider-secret-value-a'), 'Secret value leaked into the data-plane status');

  const repeated = await admin.putDataPlaneDesiredState({revision: 'rev-1', routes: [routeFor(chatModel, {providerType: 'deepseek'})]});
  assert(repeated.contentHash === first.contentHash, 'same revision was not idempotent');
  await waitFor(() => assert(applyCount >= firstCount + 2, 'periodic convergence did not reapply desired state'));
  assert(snapshot() === firstResources, 'idempotent reconciliation changed resources');

  // Rotating a credential is a catalog change: the route may only name the
  // Secret the model is bound to now, so the model moves first.
  const credentialB = await useCredential(admin, 'Provider B key', 'provider-b', 'provider-secret-value-b');
  const providerBModel = await admin.updateModel(chatModel.id, {endpoint: '/v1/responses', upstreamModel: 'provider-b', credentialId: credentialB.id});
  await admin.putDataPlaneDesiredState({revision: 'rev-2', routes: [routeFor(providerBModel)]});
  await waitForReady(admin, 'rev-2');
  assert(snapshot().includes('/v1/responses'), 'route update was not applied');
  assert(snapshot().includes("apiTokens:\n            - 'provider-secret-value-b'"), 'rotated Secret value was not inlined as an ai-proxy apiToken');
  assert(!snapshot().includes('provider-secret-value-a'), 'previous Secret value remained after Secret rotation');
  assert(!snapshot().includes('credentialRef') && !snapshot().includes(`aep-credential-${credentialB.id}`), 'Secret reference leaked into the rendered Higress resources');

  for (const key of resources.keys()) resources.set(key, 'drifted-by-operator');
  await waitFor(() => assert(!snapshot().includes('drifted-by-operator'), 'drift was not corrected'));

  const credentialC = await useCredential(admin, 'Provider C key', 'provider-c', 'provider-secret-value-c');
  const providerCModel = await admin.updateModel(chatModel.id, {endpoint: '/v1/chat', upstreamModel: 'provider-c', credentialId: credentialC.id});
  failWasm = true;
  await admin.putDataPlaneDesiredState({revision: 'rev-3', routes: [routeFor(providerCModel)]});
  await waitForStatus(admin, status => status.state === 'error' && status.errorCode === 'KUBERNETES_APPLY_FAILED');
  await waitForHealth('/readyz', 503);
  await waitForHealth('/livez', 200);
  failWasm = false;
  await waitForReady(admin, 'rev-3');
  await waitForHealth('/readyz', 200);

  const credentialD = await useCredential(admin, 'Provider D key', 'provider-d', 'provider-secret-value-d');
  const providerDModel = await admin.updateModel(chatModel.id, {endpoint: '/v1/chat', upstreamModel: 'provider-d', credentialId: credentialD.id});
  // With the credential fetcher configured, a Secret read failure is a
  // synchronization failure in its own right: the status reports
  // CREDENTIAL_UNAVAILABLE with no resource count, and the route is never
  // rendered or applied - there is no token-less fallback that reports ready.
  const beforeCredentialFailure = snapshot();
  kubeAvailable = false;
  await admin.putDataPlaneDesiredState({revision: 'rev-4', routes: [routeFor(providerDModel)]});
  await waitForStatus(admin, status => status.state === 'error' && status.errorCode === 'CREDENTIAL_UNAVAILABLE' && status.resourceCount === 0);
  assert(snapshot() === beforeCredentialFailure, 'a credential failure still rendered or applied resources');
  await waitForHealth('/readyz', 503);
  // Restoring the Secret store lets the same revision converge: the failure is
  // transient, not a poisoned desired state.
  kubeAvailable = true;
  await waitForReady(admin, 'rev-4');
  await waitForHealth('/readyz', 200);

  // Routes are normalized before they are checked against the catalog, so
  // empty endpoints and unknown ai-proxy provider types never reach the
  // renderer, whatever the catalog holds.
  await expectProblem(admin.putDataPlaneDesiredState({revision: 'malformed', routes: [{...routeFor(providerDModel), endpoint: ''}]}), 400, 'INVALID_DATA_PLANE_STATE');
  await expectProblem(admin.putDataPlaneDesiredState({revision: 'unsupported-provider', routes: [{...routeFor(providerDModel), providerType: 'unknown'}]}), 400, 'INVALID_DATA_PLANE_STATE');

  // The catalog is the only source of endpoints and Secret names, so a route
  // that invents either - or names a model the catalog would not publish - is
  // refused even though it renders.
  await expectProblem(admin.putDataPlaneDesiredState({revision: 'unknown-model', routes: [routeFor(providerDModel, {modelId: 'unpublishable'})]}), 400, 'INVALID_DATA_PLANE_STATE');
  await expectProblem(admin.putDataPlaneDesiredState({revision: 'foreign-secret', routes: [routeFor(providerDModel, {credentialRef: {name: 'provider-secrets', key: 'api-key-a', namespace: 'higress-system'}})]}), 400, 'INVALID_DATA_PLANE_STATE');
  await expectProblem(admin.putDataPlaneDesiredState({revision: 'foreign-endpoint', routes: [routeFor(providerDModel, {endpoint: 'https://foreign.example.test/v1'})]}), 400, 'INVALID_DATA_PLANE_STATE');

  // Anthropic routes: absolute endpoints only and no ai-proxy provider type -
  // both refused by normalization, before any catalog lookup.
  const anthropicPassthrough = (modelId, endpoint) => ({modelId, enabled: true, endpoint, upstreamModel: 'glm-5.3-flash', protocol: 'anthropic'});
  await expectProblem(admin.putDataPlaneDesiredState({revision: 'anthropic-relative', routes: [anthropicPassthrough('bench-anthropic', '/v1')]}), 400, 'INVALID_DATA_PLANE_STATE');
  await expectProblem(admin.putDataPlaneDesiredState({revision: 'anthropic-provider', routes: [{...anthropicPassthrough('bench-anthropic', 'https://open.bigmodel.cn/api/anthropic'), providerType: 'openai'}]}), 400, 'INVALID_DATA_PLANE_STATE');

  // Two catalog anthropic models whose client-facing path prefixes collide: the
  // renderer truncates a model ID to 40 characters for the path prefix, so the
  // second model's `-2` uniqueness suffix lands back on the first model's
  // prefix. Both routes match the catalog, so the escape hatch accepts them and
  // the reconciler is the one that refuses to render the collision.
  const collisionName = 'Anthropic Collision Passthrough Probe AA';
  const collisionModelA = await useModel(admin, {displayName: collisionName, protocol: 'anthropic', endpoint: 'https://open.bigmodel.cn/api/anthropic', upstreamModel: 'glm-5.3-flash'});
  const collisionModelB = await useModel(admin, {displayName: `${collisionName} Two`, protocol: 'anthropic', endpoint: 'https://api.anthropic.com', upstreamModel: 'glm-5.3-flash'});
  assert(collisionModelA.id.length === 40 && collisionModelB.id === `${collisionModelA.id}-2`, `anthropic collision fixture = ${collisionModelA.id} / ${collisionModelB.id}`);
  await admin.putDataPlaneDesiredState({revision: 'anthropic-collision', routes: [routeFor(collisionModelA), routeFor(collisionModelB)]});
  await waitForStatus(admin, status => status.state === 'error' && status.errorCode === 'RENDER_FAILED');
  await waitForHealth('/readyz', 503);
  // The collision models are scratch fixtures: the publication section derives
  // its route set from the whole catalog.
  await admin.deleteModel(collisionModelA.id);
  await admin.deleteModel(collisionModelB.id);
  await admin.putDataPlaneDesiredState({revision: 'rev-4', routes: [routeFor(providerDModel)]});
  await waitForReady(admin, 'rev-4');
  await waitForHealth('/readyz', 200);

  await compose('restart', 'control-service');
  await waitFor(async () => assert((await fetch(`${baseUrl}/readyz`)).ok, 'control service did not recover'));
  await waitForReady(admin, 'rev-4');

  await stopReconciler();
  const beforeRestart = applyCount;
  startReconciler();
  await waitFor(() => assert(applyCount >= beforeRestart + 2, 'reconciler restart did not converge'));

  await admin.putDataPlaneDesiredState({revision: 'rev-5', routes: [{...routeFor(providerDModel), enabled: false}]});
  await waitForReady(admin, 'rev-5');
  assert(!snapshot().includes("path: '/v1/chat'"), 'disabled route remained in Ingress');

  // The publication below derives its whole route set from the catalog, so the
  // escape-hatch fixture model leaves the catalog before it.
  await admin.deleteModel(chatModel.id);

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
  const openaiName = `aep-openai-${suffixOf('demo/catalog-chat')}`;
  const openaiFilterPath = `/apis/networking.istio.io/v1alpha3/namespaces/higress-system/envoyfilters/${openaiName}`;
  const openaiIngressPath = `/apis/networking.k8s.io/v1/namespaces/higress-system/ingresses/${openaiName}`;
  const openaiFilter = resources.get(openaiFilterPath);
  const openaiIngress = resources.get(openaiIngressPath);
  assert(resources.size === 4 && openaiFilter && openaiIngress, 'absolute OpenAI catalog endpoint must apply its ingress, upstream filter and both plugins');
  assert(openaiIngress.includes("path: '/v1'") && openaiIngress.includes("higress.io/exact-match-header-x-aep-model-id: 'catalog-chat'"), 'OpenAI ingress lost its client path or model binding');
  for (const expected of [
    'type: STRICT_DNS',
    "address: 'provider-catalog'",
    'port_value: 80',
    `name: '${openaiName}'`,
    `cluster: '${openaiName}'`,
    "host_rewrite_literal: 'provider-catalog'",
  ]) {
    assert(openaiFilter.includes(expected), `OpenAI catalog upstream is missing ${expected}`);
  }
  assert(!openaiFilter.includes('transport_socket:') && !openaiFilter.includes('regex_rewrite') && !openaiFilter.includes('request_headers_to_add'), 'HTTP OpenAI upstream must leave TLS off and path/credential handling to ai-proxy');
  assert(!openaiFilter.includes('catalog-secret-value-a') && !openaiFilter.includes('credentialRef'), 'OpenAI upstream filter contains provider credential material');
  assert(snapshot().includes("'catalog-chat': 'provider-catalog-chat'"), 'catalog modelMapping was not rendered');
  assert(snapshot().includes("apiTokens:\n            - 'catalog-secret-value-a'"), 'derived credentialRef was not resolved and inlined');
  assert(!snapshot().includes('catalog-disabled') && !snapshot().includes('catalog-local'), 'non-publishable catalog models leaked into the gateway');
  let comparison = (await admin.getDataPlaneStatus()).catalogComparison;
  assert(comparison && comparison.missing.length === 0 && comparison.extra.length === 0 && comparison.mismatched.length === 0, `published catalog still drifted: ${JSON.stringify(comparison)}`);
  assert(!JSON.stringify(await admin.getDataPlaneStatus()).includes('catalog-secret-value-a'), 'Secret value leaked into the data-plane status comparison');

  const republished = await admin.publishDataPlaneRoutes();
  assert(republished.revision === published.revision && republished.contentHash === published.contentHash, 'republishing an unchanged catalog was not idempotent');

  // Drift the escape hatch can still express: routes are pinned to the catalog,
  // so a stored route only becomes "extra" once its model leaves the catalog.
  // Publishing a route and then deleting its model produces exactly that.
  const ghostModel = await useModel(admin, {displayName: 'Ghost', endpoint: '/v1/ghost', upstreamModel: 'provider-ghost'});
  await admin.putDataPlaneDesiredState({revision: 'rev-drift', routes: [routeFor(ghostModel)]});
  await admin.deleteModel(ghostModel.id);
  comparison = (await admin.getDataPlaneStatus()).catalogComparison;
  assert(comparison.extra.includes('ghost') && comparison.missing.includes('catalog-chat'), `manual drift was not reported: ${JSON.stringify(comparison)}`);
  // Republish restores the previous catalog revision. A status that is already
  // ready for that revision can still be the observation from before this
  // drift, so wait until the ghost route is applied before publishing it away.
  await waitFor(() => assert(snapshot().includes('/v1/ghost'), 'manual drift was not applied'));
  const corrected = await admin.publishDataPlaneRoutes();
  assert(corrected.revision === published.revision, 'republishing the catalog did not restore the derived state');
  await waitForReady(admin, published.revision);
  await waitFor(() => assert(!snapshot().includes('/v1/ghost'), 'ghost route survived catalog publication'));

  // Anthropic catalog models publish into a self-contained EnvoyFilter
  // passthrough under a per-model path prefix — no ai-proxy involvement.
  const anthropicCredential = await admin.createCredential({name: 'BigModel key', service: 'bigmodel', type: 'api_key', deliveryMode: 'server_only', value: 'anthropic-secret-value', enabled: true});
  kubeSecrets.set(`aep-credential-${anthropicCredential.id}`, {'api-key': 'anthropic-secret-value'});
  await admin.createModel({displayName: 'Bench Anthropic', sourceType: 'gateway', protocol: 'anthropic', endpoint: 'https://open.bigmodel.cn/api/anthropic', upstreamModel: 'glm-5.3-flash', credentialId: anthropicCredential.id, capabilities: ['text'], isDefault: false, enabled: true});
  const anthropicPublished = await admin.publishDataPlaneRoutes();
  const derivedAnthropicRoute = anthropicPublished.routes.find(candidate => candidate.modelId === 'bench-anthropic');
  assert(derivedAnthropicRoute?.protocol === 'anthropic' && !derivedAnthropicRoute.providerType, `anthropic derived route = ${JSON.stringify(derivedAnthropicRoute)}`);
  await waitForReady(admin, anthropicPublished.revision);
  assert(resources.size === 6, `expected separate OpenAI/Anthropic ingress-filter pairs and both wasmplugins, got ${[...resources.keys()]}`);
  assert(resources.get(openaiFilterPath) === openaiFilter && resources.get(openaiIngressPath) === openaiIngress, 'Anthropic publication changed the independent OpenAI upstream resources');
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
  assert(resources.size === 4 && resources.get(openaiFilterPath) === openaiFilter && resources.get(openaiIngressPath) === openaiIngress, 'disabling Anthropic changed the independent OpenAI resource pair');
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

// Creates a Credential plus the conventional Secret the catalog derives for it
// (aep-credential-<credentialId> with a single api-key entry), so the mock
// Kubernetes API can answer every route the catalog publishes for the model.
async function useCredential(admin, name, service, value) {
  const credential = await admin.createCredential({name, service, type: 'api_key', deliveryMode: 'server_only', value, enabled: true});
  kubeSecrets.set(`aep-credential-${credential.id}`, {'api-key': value});
  return credential;
}

// Creates a publishable gateway catalog model. Its server-generated ID is what
// routes must name, so callers keep the returned model.
function useModel(admin, input) {
  return admin.createModel({sourceType: 'gateway', protocol: 'openai-compatible', capabilities: ['text'], isDefault: false, enabled: true, ...input});
}

// Mirrors deriveDataPlaneRoutes: an accepted raw route carries the catalog
// endpoint and the Secret derived from the model's credential binding. The
// rendering fields stay overridable, which is the whole point of the hatch.
function routeFor(model, overrides = {}) {
  const route = {modelId: model.id, enabled: model.enabled, endpoint: model.endpoint, upstreamModel: model.upstreamModel, protocol: model.protocol};
  if (model.protocol === 'openai-compatible') route.providerType = 'openai';
  if (model.credentialId) route.credentialRef = {name: `aep-credential-${model.credentialId}`, key: 'api-key', namespace: 'higress-system'};
  return {...route, ...overrides};
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

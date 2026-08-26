import {spawn} from 'node:child_process';
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
const composeEnv = {AEP_PORT: port, AEP_MINIO_CONSOLE_PORT: process.env.AEP_M3_MINIO_CONSOLE_PORT ?? '19008'};
const outputDir = await mkdtemp(path.join(tmpdir(), 'aep-m3-reconciler-'));
const reconcilerBinary = path.join(outputDir, process.platform === 'win32' ? 'aep-gateway-reconciler.exe' : 'aep-gateway-reconciler');
const resources = new Map();
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
  const admin = new AepClient({baseUrl, agentId: 'm3-data-plane-admin', agentVersion: 'e2e', platform: platform(), tokenStore: new MemoryTokenStore()});
  await admin.loginWithPassword({enterpriseId: 'demo', username: 'admin', password: 'change-this-admin-password'});
  startReconciler();

  const first = await admin.putDataPlaneDesiredState({revision: 'rev-1', routes: [route('chat', '/v1/chat', 'provider-a', 'api-key-a', 'provider-secrets', 'deepseek')]});
  await waitForReady(admin, 'rev-1');
  assert(resources.size === 2, 'Ingress and WasmPlugin were not both applied');
  const firstCount = applyCount;
  const firstResources = snapshot();
  assert(firstResources.includes("type: 'deepseek'"), 'DeepSeek provider type was not rendered');

  const repeated = await admin.putDataPlaneDesiredState({revision: 'rev-1', routes: [route('chat', '/v1/chat', 'provider-a', 'api-key-a', 'provider-secrets', 'deepseek')]});
  assert(repeated.contentHash === first.contentHash, 'same revision was not idempotent');
  await waitFor(() => assert(applyCount >= firstCount + 2, 'periodic convergence did not reapply desired state'));
  assert(snapshot() === firstResources, 'idempotent reconciliation changed resources');

  await admin.putDataPlaneDesiredState({revision: 'rev-2', routes: [route('chat', '/v1/responses', 'provider-b', 'api-key-b', 'provider-secrets-v2')]});
  await waitForReady(admin, 'rev-2');
  assert(snapshot().includes('/v1/responses'), 'route update was not applied');
  assert(snapshot().includes('provider-secrets-v2') && snapshot().includes('api-key-b'), 'Secret reference rotation was not applied');
  assert(!snapshot().includes('provider-secret-value'), 'provider Secret value leaked into Kubernetes resources');

  for (const key of resources.keys()) resources.set(key, 'drifted-by-operator');
  await waitFor(() => assert(!snapshot().includes('drifted-by-operator'), 'drift was not corrected'));

  failWasm = true;
  await admin.putDataPlaneDesiredState({revision: 'rev-3', routes: [route('chat', '/v1/chat', 'provider-c', 'api-key-c')]});
  await waitForStatus(admin, status => status.state === 'error' && status.errorCode === 'KUBERNETES_APPLY_FAILED');
  failWasm = false;
  await waitForReady(admin, 'rev-3');

  kubeAvailable = false;
  await admin.putDataPlaneDesiredState({revision: 'rev-4', routes: [route('chat', '/v1/chat', 'provider-d', 'api-key-d')]});
  await waitForStatus(admin, status => status.state === 'error' && status.errorCode === 'KUBERNETES_APPLY_FAILED');
  kubeAvailable = true;
  await waitForReady(admin, 'rev-4');

  await expectProblem(admin.putDataPlaneDesiredState({revision: 'malformed', routes: [{...route('bad', '', 'provider', 'key')}]}), 400, 'INVALID_DATA_PLANE_STATE');
  await expectProblem(admin.putDataPlaneDesiredState({revision: 'unsupported-provider', routes: [{...route('bad-provider', '/v1/chat', 'provider', 'key'), providerType: 'unknown'}]}), 400, 'INVALID_DATA_PLANE_STATE');

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

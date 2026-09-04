import {spawn} from 'node:child_process';
import {createHash} from 'node:crypto';
import {mkdtemp, rm} from 'node:fs/promises';
import {createServer} from 'node:http';
import {tmpdir} from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const kind = process.env.KIND ?? 'kind';
const cluster = 'aep-m3-data-plane';
const context = `kind-${cluster}`;
const controlPort = 18093;
const proxyPort = 18094;
const controlUrl = `http://127.0.0.1:${controlPort}`;
const kubeUrl = `http://127.0.0.1:${proxyPort}`;
const work = await mkdtemp(path.join(tmpdir(), 'aep-m3-kind-'));
const binary = path.join(work, process.platform === 'win32' ? 'aep-gateway-reconciler.exe' : 'aep-gateway-reconciler');
let desired = state('rev-kind-1', [{modelId: 'chat', enabled: true, endpoint: '/v1/chat', upstreamModel: 'kind-upstream', protocol: 'openai-compatible', providerType: 'deepseek', credentialRef: {name: 'provider-secrets', key: 'api-key', namespace: 'higress-system'}}]);
let observed = {};
let proxy;
const reconcilers = [];

const control = createServer(async (request, response) => {
  if (request.headers['x-aep-data-plane-token'] !== 'kind-control-token' || request.headers['x-aep-deployment-id'] !== 'demo') {
    response.writeHead(401).end();
    return;
  }
  if (request.method === 'GET') {
    response.writeHead(200, {'content-type': 'application/json'}).end(JSON.stringify(desired));
    return;
  }
  const chunks = [];
  for await (const chunk of request) chunks.push(Buffer.from(chunk));
  observed = JSON.parse(Buffer.concat(chunks).toString('utf8'));
  response.writeHead(200, {'content-type': 'application/json'}).end(JSON.stringify(observed));
});

try {
  await command(kind, ['delete', 'cluster', '--name', cluster], {}, true);
  await command(kind, ['create', 'cluster', '--name', cluster, '--wait', '120s']);
  await command('kubectl', ['--context', context, 'create', 'namespace', 'higress-system']);
  await command('kubectl', ['--context', context, 'apply', '-f', path.join(root, 'tests', 'e2e', 'fixtures', 'higress-wasmplugin-crd.yaml')]);
  await command('kubectl', ['--context', context, '-n', 'higress-system', 'create', 'service', 'clusterip', 'aep-model-gateway', '--tcp=80:8080']);
  await listen(control, controlPort);
  proxy = spawn('kubectl', ['--context', context, 'proxy', '--port', String(proxyPort), '--accept-hosts=.*'], {cwd: root, stdio: 'ignore', shell: false});
  await waitFor(async () => assert((await fetch(`${kubeUrl}/version`)).ok, 'kubectl proxy is not ready'));
  await command('go', ['build', '-o', binary, './services/gateway-reconciler/cmd/server']);
  reconcilers.push(startReconciler(18095, 'a'), startReconciler(18096, 'b'));
  await waitFor(() => assert(observed.state === 'ready' && observed.observedRevision === 'rev-kind-1', `status is ${JSON.stringify(observed)}`));

  const ingressName = `aep-model-gateway-${suffix('demo')}`;
  const pluginName = `aep-ai-proxy-${suffix('demo')}`;
  const ingress = JSON.parse(await output('kubectl', ['--context', context, '-n', 'higress-system', 'get', 'ingress', ingressName, '-o', 'json']));
  assert(ingress.spec.rules[0].http.paths[0].path === '/v1/chat', 'real Kubernetes Ingress route was incorrect');
  const plugin = JSON.parse(await output('kubectl', ['--context', context, '-n', 'higress-system', 'get', 'wasmplugin', pluginName, '-o', 'json']));
  assert(plugin.spec.matchRules[0].config.credentialRef.name === 'provider-secrets', 'Higress resource omitted the Secret reference');
  assert(plugin.spec.matchRules[0].config.provider.type === 'deepseek', 'Higress resource did not select the DeepSeek provider');

  await command('kubectl', ['--context', context, '-n', 'higress-system', 'patch', 'ingress', ingressName, '--type=json', '-p', '[{"op":"replace","path":"/spec/rules/0/http/paths/0/path","value":"/drifted"}]']);
  await waitFor(async () => {
    const current = JSON.parse(await output('kubectl', ['--context', context, '-n', 'higress-system', 'get', 'ingress', ingressName, '-o', 'json']));
    assert(current.spec.rules[0].http.paths[0].path === '/v1/chat', 'server-side apply did not correct drift');
  });

  desired = state('rev-kind-2', [{...desired.routes[0], enabled: false}]);
  await waitFor(() => assert(observed.state === 'ready' && observed.observedRevision === 'rev-kind-2', `status is ${JSON.stringify(observed)}`));
  const disabled = JSON.parse(await output('kubectl', ['--context', context, '-n', 'higress-system', 'get', 'wasmplugin', pluginName, '-o', 'json']));
  assert(disabled.spec.matchRules === null || disabled.spec.matchRules.length === 0, 'disabled route remained in Higress match rules');
  console.log('AEP M3 kind/Higress-compatible server-side apply scenario passed.');
} finally {
  for (const child of reconcilers) await stop(child);
  await stop(proxy);
  await new Promise(resolve => control.close(resolve));
  await command(kind, ['delete', 'cluster', '--name', cluster], {}, true);
  await rm(work, {recursive: true, force: true});
}

function state(revision, routes) {
  const sorted = [...routes].sort((a, b) => a.modelId.localeCompare(b.modelId));
  const contentHash = createHash('sha256').update(JSON.stringify({revision, routes: sorted})).digest('hex');
  return {deploymentId: 'demo', revision, publishedAt: new Date().toISOString(), contentHash, routes: sorted};
}

function suffix(value) {
  return `${value}-${createHash('sha256').update(value).digest('hex').slice(0, 8)}`;
}

function startReconciler(port, instance) {
  return spawn(binary, [], {cwd: root, env: {...process.env,
    AEP_RECONCILER_CONTROL_URL: controlUrl,
    AEP_DATA_PLANE_RECONCILER_TOKEN: 'kind-control-token',
    AEP_RECONCILER_TENANTS: 'demo',
    AEP_RECONCILER_OUTPUT_DIR: path.join(work, instance),
    AEP_RECONCILER_INTERVAL: '500ms',
    AEP_RECONCILER_ADDRESS: `127.0.0.1:${port}`,
    AEP_RECONCILER_KUBERNETES_URL: kubeUrl,
    AEP_RECONCILER_KUBERNETES_TOKEN: 'kubectl-proxy-authenticates-upstream',
  }, stdio: 'ignore', shell: false});
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

function listen(server, port) {
  return new Promise((resolve, reject) => { server.once('error', reject); server.listen(port, '127.0.0.1', resolve); });
}

async function stop(child) {
  if (!child || child.exitCode !== null) return;
  child.kill();
  await new Promise(resolve => { const timer = setTimeout(resolve, 5_000); child.once('exit', () => { clearTimeout(timer); resolve(); }); });
}

function output(executable, args) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {cwd: root, stdio: ['ignore', 'pipe', 'pipe'], shell: false});
    const stdout = [], stderr = [];
    child.stdout.on('data', chunk => stdout.push(Buffer.from(chunk)));
    child.stderr.on('data', chunk => stderr.push(Buffer.from(chunk)));
    child.on('error', reject);
    child.on('exit', code => code === 0 ? resolve(Buffer.concat(stdout).toString('utf8')) : reject(new Error(Buffer.concat(stderr).toString('utf8'))));
  });
}

function command(executable, args, extraEnv = {}, allowFailure = false) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {cwd: root, env: {...process.env, ...extraEnv}, stdio: 'inherit', shell: false});
    child.on('error', reject);
    child.on('exit', code => code === 0 || allowFailure ? resolve() : reject(new Error(`${executable} ${args.join(' ')} exited with ${code}`)));
  });
}

function assert(condition, message) { if (!condition) throw new Error(message); }

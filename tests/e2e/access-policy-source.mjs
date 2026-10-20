// Explicit, isolated delivery test. Does not switch authorizer or touch other clusters.
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {randomBytes} from 'node:crypto';
import {mkdtemp, readFile, rm, writeFile} from 'node:fs/promises';
import https from 'node:https';
import {tmpdir} from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const work = await mkdtemp(path.join(tmpdir(), 'aep-policy-source-'));
const cluster = 'aep-policy-source-' + randomBytes(6).toString('hex');
const kubeconfig = path.join(work, 'kubeconfig');
const kind = process.env.KIND ?? 'kind';
const openssl = process.env.OPENSSL ?? 'openssl';
const namespace = 'aep-system';
const host = 'aep-access-policy-source.aep-system.svc';
const report = {scope: 'native ConfigMap watch -> static HTTPS; not business authorization adoption', checks: [], adoptionGate: false,
  networkPolicyEnforcementTested: false};
const forwards = [];
let owned = false;
let ca;
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
function command(binary, args, {input, timeout = 180_000, allowDenied = false} = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(binary, args, {cwd: root, env: {...process.env, KUBECONFIG: kubeconfig}, shell: false, stdio: ['pipe', 'pipe', 'pipe']});
    let out = '', errors = '';
    const timer = setTimeout(() => {child.kill(); reject(new Error(binary + ' timed out'));}, timeout);
    child.stdout.on('data', data => out += data);
    child.stderr.on('data', data => errors += data);
    child.on('error', error => {clearTimeout(timer); reject(error);});
    child.on('exit', code => {
      clearTimeout(timer);
      if (code === 0 || allowDenied && code === 1 && out.trim() === 'no' && errors.trim() === '') resolve(out.trim());
      else reject(new Error(binary + ' failed: ' + errors));
    });
    child.stdin.end(input);
  });
}
const kubectl = (args, options) => command('kubectl', ['--kubeconfig', kubeconfig, '--context', 'kind-' + cluster, ...args], options);
const apply = items => kubectl(['apply', '-f', '-'], {input: JSON.stringify({apiVersion: 'v1', kind: 'List', items})});
const pods = async () => JSON.parse(await kubectl(['-n', namespace, 'get', 'pods', '-l', 'app.kubernetes.io/name=aep-access-policy-source', '-o', 'json'])).items;
async function until(check, timeout = 45_000) {
  let last;
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    try {return await check();} catch (error) {last = error; await delay(250);}
  }
  throw last ?? new Error('Policy file convergence timed out');
}
async function forward(pod) {
  const child = spawn('kubectl', ['--kubeconfig', kubeconfig, '--context', 'kind-' + cluster, '-n', namespace,
    'port-forward', '--address', '127.0.0.1', 'pod/' + pod, ':8443'], {shell: false});
  forwards.push(child);
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('Policy source port-forward timeout')), 20_000);
    child.stdout.on('data', data => {
      const port = /127\.0\.0\.1:(\d+)/.exec(String(data))?.[1];
      if (port) {clearTimeout(timer); resolve(Number(port));}
    });
    child.stderr.on('data', () => {});
    child.on('error', error => {clearTimeout(timer); reject(error);});
    child.on('exit', () => {clearTimeout(timer); reject(new Error('Policy source port-forward ended'));});
  });
}
function request(port, url, {method = 'GET', servername = host, trustedCA = ca} = {}) {
  return new Promise((resolve, reject) => {
    const req = https.request({host: '127.0.0.1', port, servername, ca: trustedCA, rejectUnauthorized: true,
      path: url, method, timeout: 5000}, response => {
      let body = '';
      response.on('data', data => body += data);
      response.on('end', () => resolve({status: response.statusCode, body, cache: response.headers['cache-control']}));
    });
    req.on('error', reject);
    req.on('timeout', () => req.destroy(new Error('HTTPS policy request timeout')));
    req.end();
  });
}
function record(name, details = {}) {report.checks.push({name, ...details}); console.log('PASS: ' + name);}
function config(name, key, body, selected = true) {
  return {apiVersion: 'v1', kind: 'ConfigMap', metadata: {name, namespace,
    labels: selected ? {'gateway.aep.zhiyuan.io/access-policy': 'true'} : {}}, data: {[key]: body}};
}
try {
  const rendered = await command('kubectl', ['kustomize', 'deploy/kubernetes/access-policy-source']);
  const images = [...rendered.matchAll(/^\s+image: (\S+)/gm)].map(match => match[1]);
  assert.equal(images.length, 2);
  assert(images.every(image => /@sha256:[a-f0-9]{64}$/.test(image)), 'All source images must be immutable');
  report.images = images;
  // Generated disposable PKI stays outside the repository and is never logged.
  const file = name => path.join(work, name);
  await command(openssl, ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', file('ca.key'), '-out', file('ca.crt'),
    '-days', '1', '-subj', '/CN=disposable-policy-source-ca']);
  await command(openssl, ['req', '-newkey', 'rsa:2048', '-nodes', '-keyout', file('server.key'), '-out', file('server.csr'),
    '-subj', '/CN=' + host]);
  await writeFile(file('server.ext'), 'subjectAltName=DNS:' + host + '\nbasicConstraints=CA:FALSE\nkeyUsage=digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\n');
  await command(openssl, ['x509', '-req', '-in', file('server.csr'), '-CA', file('ca.crt'), '-CAkey', file('ca.key'),
    '-CAcreateserial', '-out', file('server.crt'), '-days', '1', '-extfile', file('server.ext')]);
  ca = await readFile(file('ca.crt'));
  assert(!(await command(kind, ['get', 'clusters'])).split(/\s+/).includes(cluster));
  owned = true;
  console.log('Creating owned policy delivery cluster');
  await command(kind, ['create', 'cluster', '--name', cluster, '--kubeconfig', kubeconfig, '--wait', '120s', '--image',
    'kindest/node@sha256:3489c7674813ba5d8b1a9977baea8a6e553784dab7b84759d1014dbd78f7ebd5']);
  for (const [i, image] of images.entries()) {
    const tag = image.split('@')[0], digest = image.split('@')[1];
    const inspection = JSON.parse(await command('docker', ['image', 'inspect', tag]));
    assert(inspection[0].RepoDigests.some(ref => ref.endsWith('@' + digest)));
    const archive = file('image-' + i + '.tar');
    await command('docker', ['image', 'save', '-o', archive, tag]);
    await command('docker', ['cp', archive, cluster + '-control-plane:/aep-policy-image.tar']);
    await command('docker', ['exec', cluster + '-control-plane', 'ctr', '-n', 'k8s.io', 'images', 'import',
      '--digests', '--platform', 'linux/amd64', '/aep-policy-image.tar']);
    await command('docker', ['exec', cluster + '-control-plane', 'rm', '--', '/aep-policy-image.tar']);
    await rm(archive);
  }
  await apply([{apiVersion: 'v1', kind: 'Namespace', metadata: {name: namespace,
    labels: {'pod-security.kubernetes.io/enforce': 'restricted'}}}]);
  const secret = await kubectl(['-n', namespace, 'create', 'secret', 'tls', 'aep-access-policy-tls',
    '--cert=' + file('server.crt'), '--key=' + file('server.key'), '--dry-run=client', '-o', 'json']);
  await kubectl(['apply', '-f', '-'], {input: secret});
  await kubectl(['apply', '-f', '-'], {input: rendered});
  await kubectl(['-n', namespace, 'rollout', 'status', 'deployment/aep-access-policy-source', '--timeout=150s']);
  const initial = await pods();
  assert.equal(initial.length, 2);
  const ports = await Promise.all(initial.map(pod => forward(pod.metadata.name)));
  const key = 'access-' + 'a'.repeat(64) + '.rego';
  const ignored = 'access-' + 'b'.repeat(64) + '.rego';
  const first = 'allow { false }\npublication = 1\n';
  const second = 'allow { false }\npublication = 2\n';
  for (const port of ports) {
    assert.equal((await request(port, '/' + key)).status, 404);
    await assert.rejects(() => request(port, '/healthz', {servername: 'wrong-source.invalid'}), /Hostname\/IP does not match/);
    await assert.rejects(() => request(port, '/healthz', {trustedCA: []}), error =>
      ['UNABLE_TO_VERIFY_LEAF_SIGNATURE', 'UNABLE_TO_GET_ISSUER_CERT_LOCALLY'].includes(error.code));
  }
  record('Both source replicas require valid TLS name/CA and missing policy returns 404');
  await apply([config('delivery-owned', key, first), config('delivery-unselected', ignored, first, false)]);
  for (const port of ports) await until(async () => {
    const result = await request(port, '/' + key);
    assert.equal(result.status, 200); assert.equal(result.body, first); assert.equal(result.cache, 'no-store');
    assert.equal((await request(port, '/' + ignored)).status, 404);
  });
  record('New labeled ConfigMap files appear on both running replicas without a volume list or rollout');
  const started = Date.now();
  await apply([config('delivery-owned', key, second)]);
  for (const port of ports) await until(async () => assert.equal((await request(port, '/' + key)).body, second));
  assert.deepEqual((await pods()).map(p => p.metadata.uid).sort(), initial.map(p => p.metadata.uid).sort());
  for (const pod of await pods()) assert(pod.status.containerStatuses.every(c => c.restartCount === 0));
  record('Native watch updates both HTTPS files without Pod restart', {propagationMs: Date.now() - started});
  for (const port of ports) {
    for (const url of ['/', '/nginx.conf', '/etc/aep-tls/tls.key', '/' + key + '.url']) assert.equal((await request(port, url)).status, 404);
    assert.equal((await request(port, '/' + key, {method: 'PUT'})).status, 403);
  }
  const identity = 'system:serviceaccount:aep-system:aep-access-policy-source';
  for (const [verb, resource, ns] of [['get', 'secrets', namespace], ['create', 'configmaps', namespace], ['get', 'configmaps', 'default']]) {
    assert.equal(await kubectl(['auth', 'can-i', verb, resource, '-n', ns, '--as=' + identity], {allowDenied: true}), 'no');
  }
  record('Static server exposes only policy filenames; source identity cannot read Secrets/write resources/read another namespace');
  await kubectl(['-n', namespace, 'delete', 'pod', initial[0].metadata.name, '--wait=true']);
  await kubectl(['-n', namespace, 'rollout', 'status', 'deployment/aep-access-policy-source', '--timeout=120s']);
  const recovered = (await pods()).filter(pod => pod.metadata.uid !== initial[0].metadata.uid);
  assert.equal(recovered.length, 2);
  const newPod = recovered.find(pod => !initial.some(old => old.metadata.uid === pod.metadata.uid));
  assert(newPod);
  const recoveredPort = await forward(newPod.metadata.name);
  assert.equal((await request(recoveredPort, '/' + key)).body, second);
  record('Replacement source starts from current ConfigMap contents rather than an old local file');
  await kubectl(['-n', namespace, 'delete', 'configmap', 'delivery-owned']);
  for (const port of [ports[1], recoveredPort]) await until(async () => assert.equal((await request(port, '/' + key)).status, 404));
  record('Deleted policy is removed from every current source replica');
  report.completed = true;
} catch (error) {
  report.completed = false; report.error = error.message;
  if (owned) {
    try {report.pods = await kubectl(['-n', namespace, 'get', 'pods', '-o', 'wide'], {timeout: 10_000});}
    catch {report.pods = 'Unavailable';}
  }
  throw error;
} finally {
  for (const child of forwards) child.kill();
  try {
    if (owned) {
      await command(kind, ['delete', 'cluster', '--name', cluster]);
      assert(!(await command(kind, ['get', 'clusters'])).split(/\s+/).includes(cluster)); report.cleaned = true;
    }
  } finally {
    await rm(work, {recursive: true, force: true});
    if (process.env.AEP_POLICY_SOURCE_REPORT) await writeFile(process.env.AEP_POLICY_SOURCE_REPORT, JSON.stringify(report, null, 2) + '\n');
    console.log(JSON.stringify(report, null, 2));
  }
}

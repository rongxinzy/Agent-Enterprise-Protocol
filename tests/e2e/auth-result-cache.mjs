// Opt-in component feasibility probe; never changes AEP/Higress runtime defaults.
// Creates/deletes ONLY its own fresh Kind cluster, including third-party CRDs.
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {createHash, randomBytes} from 'node:crypto';
import {mkdtemp, readFile, writeFile, rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import path from 'node:path';
import http from 'node:http';
import {fileURLToPath} from 'node:url';
import {namespace, sourceRevision, crdHash, image, resources, policy} from './auth-cache/resources.mjs';
import {sharedGateway, gatewayImage} from './auth-cache/shared-gateway.mjs';
import {aepJwt} from './auth-cache/aep-jwt.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const kind = process.env.KIND ?? 'kind';
const cluster = 'aep-auth-cache-' + randomBytes(6).toString('hex');
const work = await mkdtemp(path.join(tmpdir(), 'aep-auth-cache-'));
const kubeconfig = path.join(work, 'kubeconfig');
const forwards = [];
let owned = false;
const report = {component: 'Authorino v0.26.4', sourceRevision, image,
  scope: 'isolated component probe, not AEP/Higress integration',
  cacheSemantics: {scope: 'replica', coldMiss: 'full-evaluation', warmHit: 'identity-and-authorization-reuse'},
  statusWriter: 'native-lease-leader',
  checks: [], adoptionGate: false};
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function command(binary, args, {input, timeout = 180_000} = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(binary, args, {cwd: root, env: {...process.env, KUBECONFIG: kubeconfig}, shell: false, stdio: ['pipe', 'pipe', 'pipe']});
    let output = '', errors = '';
    const timer = setTimeout(() => {child.kill(); reject(new Error(binary + ' timed out'));}, timeout);
    child.stdout.on('data', data => output += data);
    child.stderr.on('data', data => errors += data);
    child.on('error', error => {clearTimeout(timer); reject(error);});
    child.on('exit', code => {clearTimeout(timer); code === 0 ? resolve(output.trim()) : reject(new Error(binary + ' failed: ' + errors));});
    child.stdin.end(input);
  });
}
const kubectl = (args, options) => command('kubectl', ['--kubeconfig', kubeconfig, '--context', 'kind-' + cluster, ...args], options);
const apply = objects => kubectl(['apply', '-f', '-'], {input: JSON.stringify({apiVersion: 'v1', kind: 'List', items: objects})});
async function until(check, timeout = 45_000) {
  const deadline = Date.now() + timeout;
  let last;
  while (Date.now() < deadline) {
    try {return await check();} catch (error) {last = error; await delay(250);}
  }
  throw last ?? new Error('Convergence timed out');
}
async function forward(target, remote) {
  const child = spawn('kubectl', ['--kubeconfig', kubeconfig, '--context', 'kind-' + cluster,
    '-n', namespace, 'port-forward', '--address', '127.0.0.1', target, ':' + remote], {shell: false});
  forwards.push(child);
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('Port-forward timeout')), 20_000);
    child.stdout.on('data', data => {
      const port = /Forwarding from 127\.0\.0\.1:(\d+)/.exec(String(data))?.[1];
      if (port) {clearTimeout(timer); resolve(Number(port));}
    });
    child.stderr.on('data', () => {});
    child.on('error', error => {clearTimeout(timer); reject(error);});
    child.on('exit', () => {clearTimeout(timer); reject(new Error('Port-forward exited'));});
  });
}
async function request(port, host, token, model = 'model-a') {
  return new Promise((resolve, reject) => {
    const req = http.request({host: '127.0.0.1', port, path: '/check', method: 'POST', timeout: 5000,
      headers: {host, authorization: 'Bearer ' + token, 'content-type': 'application/json', 'x-aep-model-id': model}}, res => {
      res.resume();
      res.on('end', () => {
        try {resolve({status: res.statusCode, ...JSON.parse(res.headers['x-probe-witness'] ?? '{}')});}
        catch {reject(new Error('Invalid JSON probe witness'));}
      });
    });
    req.on('error', reject);
    req.on('timeout', () => req.destroy(new Error('Probe request timeout')));
    req.end('{}');
  });
}
function record(name, details = {}) {report.checks.push({name, ...details}); console.log((details.targetSatisfied === false ? 'OBSERVED: ' : 'PASS: ') + name);}
try {
  let crd;
  if (process.env.AEP_AUTH_CACHE_CRD_PATH) {
    crd = await readFile(process.env.AEP_AUTH_CACHE_CRD_PATH, 'utf8');
  } else {
    const response = await fetch('https://raw.githubusercontent.com/Kuadrant/authorino/' + sourceRevision + '/install/crd/authorino.kuadrant.io_authconfigs.yaml', {signal: AbortSignal.timeout(30_000)});
    assert(response.ok, 'Cannot download pinned third-party CRD');
    crd = await response.text();
  }
  assert.equal(createHash('sha256').update(crd.replace(/\r\n/g, '\n')).digest('hex'), crdHash, 'Pinned CRD integrity');
  assert(!(await command(kind, ['get', 'clusters'])).split(/\s+/).includes(cluster));
  owned = true;
  console.log('Creating isolated component cluster');
  await command(kind, ['create', 'cluster', '--name', cluster, '--kubeconfig', kubeconfig, '--wait', '120s',
    ...(process.env.AEP_AUTH_CACHE_NODE_IMAGE ? ['--image', process.env.AEP_AUTH_CACHE_NODE_IMAGE] : [])]);
  await command('docker', ['pull', 'node:24-alpine']);
  const imageArchive = path.join(work, 'images.tar');
  console.log('Loading single-platform component images');
  // Let kubelet pull Authorino by its immutable digest. Importing an OCI index
  // under a digest with Docker/containerd can leave dangling import references.
  await command('docker', ['image', 'save', '--platform=linux/amd64', '-o', imageArchive, 'node:24-alpine']);
  await command(kind, ['load', 'image-archive', '--name', cluster, imageArchive]);
  await kubectl(['apply', '-f', '-'], {input: crd});
  await kubectl(['wait', '--for=condition=Established', 'crd/authconfigs.authorino.kuadrant.io', '--timeout=60s']);
  const mock = await readFile(path.join(root, 'tests/e2e/auth-cache/mock-introspection.mjs'), 'utf8');
  await apply(resources(mock));
  console.log('Waiting for two independent component replicas');
  for (const name of ['introspection', 'authorino-a', 'authorino-b']) {
    await kubectl(['-n', namespace, 'rollout', 'status', 'deployment/' + name, '--timeout=120s']);
  }
  const ports = await Promise.all(['authorino-a', 'authorino-b'].map(name => forward('deployment/' + name, 5001)));
  const statsPort = await forward('deployment/introspection', 8080);
  const stats = async () => (await fetch('http://127.0.0.1:' + statsPort + '/stats', {signal: AbortSignal.timeout(5000)})).json();
  async function publish(objects) {
    await apply(objects);
    for (const object of objects) await kubectl(['-n', namespace, 'wait', '--for=condition=Ready',
      'authconfig/' + object.metadata.name, '--timeout=45s']);
    // Initial status writes are themselves watch events. Let their re-indexing
    // settle before measuring the steady-state cache; replica witnesses below
    // independently establish data-plane adoption after each policy change.
    await delay(750);
  }
  const call = (port, subject, model, token) => request(port, subject + '.probe.invalid', token ?? 'probe-' + subject + '-session', model);
  async function allowed(port, subject, revision = '1', model = 'model-a', token) {
    return until(async () => {const result = await call(port, subject, model, token);
      assert.equal(result.status, 200); assert.equal(result.revision, revision);
      assert(result.identity && result.decision, 'Both identity and authorization witnesses required'); return result;});
  }

  // A shared AuthConfig rebuilds every evaluator cache, including unrelated subjects.
  await publish([policy('shared')]);
  const warm = [];
  for (const port of ports) {
    const alice = await allowed(port, 'shared', '1', 'model-a', 'probe-alice-session');
    const bob = await allowed(port, 'shared', '1', 'model-a', 'probe-bob-session');
    assert.deepEqual(await allowed(port, 'shared', '1', 'model-a', 'probe-alice-session'), alice);
    assert.deepEqual(await allowed(port, 'shared', '1', 'model-a', 'probe-bob-session'), bob);
    warm.push({alice, bob});
  }
  assert.equal((await stats()).calls['session-a'], 2);
  assert.equal((await stats()).calls['session-b'], 2);
  record('Repeated requests reuse identity and authorization on both replicas');
  await publish([policy('shared', {model: 'model-b', revision: '2'})]);
  for (const [i, port] of ports.entries()) {
    const bob = await allowed(port, 'shared', '2', 'model-a', 'probe-bob-session');
    assert.notEqual(bob.identity, warm[i].bob.identity);
    assert.notEqual(bob.decision, warm[i].bob.decision);
  }
  record('Known limitation: shared AuthConfig updates also invalidate unrelated subjects', {targetSatisfied: false});

  // Per-principal CRs isolate caches, but do not prove production request binding/scalability.
  await publish([policy('alice', {subject: 'alice'}), policy('bob', {subject: 'bob'})]);
  const bobWarm = [];
  for (const port of ports) {
    await allowed(port, 'alice');
    bobWarm.push(await allowed(port, 'bob'));
  }
  const beforeUpdate = await stats();
  await publish([policy('alice', {subject: 'alice', model: 'model-b', revision: '2'})]);
  for (const [i, port] of ports.entries()) {
    await allowed(port, 'alice', '2', 'model-b');
    assert.equal((await call(port, 'alice', 'model-a')).status, 403);
    assert.deepEqual(await allowed(port, 'bob'), bobWarm[i]);
  }
  const afterUpdate = await stats();
  assert.equal(afterUpdate.calls['session-a'] - beforeUpdate.calls['session-a'], 2);
  assert.equal(afterUpdate.calls['session-b'], beforeUpdate.calls['session-b']);
  record('Separate principal CR updates reauthenticate Alice only, reject old model, preserve Bob on both replicas');
  for (const port of ports) {
    assert.equal((await call(port, 'bob', 'model-a', 'probe-alice-session')).status, 403);
    assert.equal((await call(port, 'alice', 'model-b', 'probe-invalid-session')).status, 401);
  }
  record('Cross-principal reuse and unknown credentials rejected');
  await publish([policy('alice', {subject: 'alice', model: 'model-b', revision: '3', sessions: []})]);
  for (const port of ports) await until(async () => assert.equal((await call(port, 'alice', 'model-b')).status, 403));
  for (const [i, port] of ports.entries()) assert.deepEqual(await allowed(port, 'bob'), bobWarm[i]);
  record('Session withdrawal converges on both replicas without invalidating Bob');

  async function leader() {
    const leases = JSON.parse(await kubectl(['-n', namespace, 'get', 'leases', '-o', 'json']));
    assert.equal(leases.items.length, 1);
    const holder = leases.items[0].spec.holderIdentity;
    const pods = JSON.parse(await kubectl(['-n', namespace, 'get', 'pods', '-o', 'json']));
    const pod = pods.items.find(item => holder?.startsWith(item.metadata.name + '_'));
    assert(pod && pod.status.conditions?.some(item => item.type === 'Ready' && item.status === 'True'));
    const replica = ['authorino-a', 'authorino-b'].indexOf(pod.metadata.labels.app);
    assert(replica >= 0);
    return {holder, replica};
  }
  // Status leadership is native Authorino behavior. All replicas still reconcile
  // and serve; only one publishes status so a cold process cannot reset peers.
  const bobCurrent = [...bobWarm];
  for (const mode of ['follower', 'leader']) {
    const previousLeader = await until(leader);
    const index = mode === 'leader' ? previousLeader.replica : 1 - previousLeader.replica;
    const name = ['authorino-a', 'authorino-b'][index];
    const beforeRestart = await stats();
    await kubectl(['-n', namespace, 'delete', 'pods', '-l', 'app=' + name, '--wait=true']);
    await kubectl(['-n', namespace, 'rollout', 'status', 'deployment/' + name, '--timeout=120s']);
    const elected = await until(async () => {
      const current = await leader();
      if (mode === 'leader') assert.notEqual(current.holder, previousLeader.holder);
      else assert.equal(current.holder, previousLeader.holder);
      return current;
    });
    await delay(750);
    ports[index] = await forward('deployment/' + name, 5001);
    await until(async () => assert.equal((await call(ports[index], 'alice', 'model-b')).status, 403));
    const cold = await allowed(ports[index], 'bob');
    assert.notEqual(cold.identity, bobCurrent[index].identity);
    assert.notEqual(cold.decision, bobCurrent[index].decision);
    for (let i = 0; i < 3; i++) assert.deepEqual(await allowed(ports[index], 'bob'), cold);
    assert.deepEqual(await allowed(ports[1 - index], 'bob'), bobCurrent[1 - index]);
    assert.equal((await stats()).calls['session-b'] - beforeRestart.calls['session-b'], 1);
    bobCurrent[index] = cold;
    record('Restarted ' + mode + ' denies revoked access and evaluates Bob once before three reuses; surviving replica stays warm',
      {coldAuthentications: 1, warmReuses: 3, cacheScope: 'replica', leadershipChanged: elected.holder !== previousLeader.holder});
  }

  await publish([policy('alice', {subject: 'alice', revision: '4'})]);
  const cachedShort = await allowed(ports[0], 'alice', '4', 'model-a', 'probe-short-session');
  await until(async () => assert(Date.now() / 1000 >= (await stats()).expirations['session-short'] + 0.2));
  assert.deepEqual(await call(ports[0], 'alice', 'model-a', 'probe-short-session'), cachedShort);
  record('Known limitation: evaluator TTL alone reuses an expired introspection result', {targetSatisfied: false});

  await publish([policy('alice', {subject: 'alice', revision: '5', freshness: true})]);
  await allowed(ports[0], 'alice', '5', 'model-a', 'probe-guarded-session');
  const guardedCalls = (await stats()).calls['session-guarded'];
  await until(async () => assert(Date.now() / 1000 >= (await stats()).expirations['session-guarded'] + 0.2));
  assert.equal((await call(ports[0], 'alice', 'model-a', 'probe-guarded-session')).status, 403);
  assert.equal((await stats()).calls['session-guarded'], guardedCalls);
  record('Uncached native expiration guard rejects cached expired result without introspection');
  if (process.env.AEP_AUTH_CACHE_GATEWAY === '1') {
    report.gatewayImage = gatewayImage;
    await sharedGateway({apply, publish, kubectl, forward, stats, record, until});
  }
  if (process.env.AEP_AUTH_CACHE_JWT === '1') {
    await aepJwt({command, publish, request, ports, statsPort, record, until});
  }
  report.completed = true;
} catch (error) {
  report.completed = false;
  report.error = error.message;
  throw error;
} finally {
  for (const child of forwards) child.kill();
  try {
    if (owned) {
      await command(kind, ['delete', 'cluster', '--name', cluster]);
      assert(!(await command(kind, ['get', 'clusters'])).split(/\s+/).includes(cluster));
      report.cleaned = true;
    }
  } finally {
    await rm(work, {recursive: true, force: true});
    if (process.env.AEP_AUTH_CACHE_REPORT) await writeFile(process.env.AEP_AUTH_CACHE_REPORT, JSON.stringify(report, null, 2) + '\n');
    console.log(JSON.stringify(report, null, 2));
  }
}

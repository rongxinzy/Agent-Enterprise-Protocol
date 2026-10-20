// Opt-in CR -> real Operator -> native Authorino acceptance, never default traffic.
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {createHash, randomBytes} from 'node:crypto';
import {mkdtemp, readFile, rm, writeFile} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import http from 'node:http';
import {resources, image, crdHash} from './auth-cache/resources.mjs';
import {httpsPolicySource, sourceFailureChecks} from './auth-cache/https-policy-source.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const kind = process.env.KIND ?? 'kind';
const cluster = 'aep-native-access-' + randomBytes(6).toString('hex');
const work = await mkdtemp(path.join(tmpdir(), 'aep-native-access-'));
const kubeconfig = path.join(work, 'kubeconfig');
const env = {...process.env, KUBECONFIG: kubeconfig};
const namespace = 'aep-system';
const requireEntitlement = process.env.AEP_NATIVE_ACCESS_REQUIRE_ENTITLEMENT === 'true';
const sourceDirectory = process.env.AEP_NATIVE_ACCESS_POLICY_SOURCE_DIR;
const forwards = [];
const report = {scope: 'CR -> Operator -> native Authorino; not business DB/Higress adoption', requireEntitlement,
  httpsPolicySource: Boolean(sourceDirectory), checks: [], adoptionGate: false};
let owned = false;
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
function command(binary, args, {input, timeout = 240_000, commandEnv = env} = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(binary, args, {cwd: root, env: commandEnv, shell: false, stdio: ['pipe', 'pipe', 'pipe']});
    let out = '', errors = '';
    const timer = setTimeout(() => {child.kill(); reject(new Error(binary + ' timeout'));}, timeout);
    child.stdout.on('data', data => out += data);
    child.stderr.on('data', data => errors += data);
    child.on('error', error => {clearTimeout(timer); reject(error);});
    child.on('exit', code => {clearTimeout(timer); code === 0 ? resolve(out.trim()) : reject(new Error(binary + ' failed: ' + errors));});
    child.stdin.end(input);
  });
}
const kubectl = (args, options) => command('kubectl', ['--kubeconfig', kubeconfig, '--context', 'kind-' + cluster, ...args], options);
const apply = objects => kubectl(['apply', '-f', '-'], {input: JSON.stringify({apiVersion: 'v1', kind: 'List', items: objects})});
const get = async (type, name) => JSON.parse(await kubectl(['-n', namespace, 'get', type, name, '-o', 'json']));
async function until(check, timeout = 120_000) {
  const deadline = Date.now() + timeout;
  let last;
  while (Date.now() < deadline) {
    try {return await check();} catch (error) {last = error; await delay(500);}
  }
  throw last ?? new Error('Native projection convergence timeout');
}
async function forward(target, remote) {
  const child = spawn('kubectl', ['--kubeconfig', kubeconfig, '--context', 'kind-' + cluster, '-n', namespace,
    'port-forward', '--address', '127.0.0.1', target, ':' + remote], {shell: false});
  forwards.push(child);
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('Port-forward timeout')), 20_000);
    child.stdout.on('data', data => {
      const port = /127\.0\.0\.1:(\d+)/.exec(String(data))?.[1];
      if (port) {clearTimeout(timer); resolve(Number(port));}
    });
    child.stderr.on('data', () => {});
    child.on('error', error => {clearTimeout(timer); reject(error);});
    child.on('exit', () => {clearTimeout(timer); reject(new Error('Port-forward ended'));});
  });
}
function request(port, host, token, model = 'model-a', body) {
  return new Promise((resolve, reject) => {
    const req = http.request({host: '127.0.0.1', port, path: '/check', method: 'POST', timeout: 5000,
      headers: {host, authorization: 'Bearer ' + token, 'content-type': 'application/json', 'x-aep-model-id': 'model-a'}}, res => {
      res.resume();
      res.on('end', () => {
        try {resolve({status: res.statusCode, observation: JSON.parse(res.headers['x-aep-access-observation'] ?? '{}')});}
        catch (error) {reject(error);}
      });
    });
    req.on('error', reject); req.on('timeout', () => req.destroy(new Error('Native request timeout')));
    req.end(body ?? JSON.stringify({model, messages: [{role: 'user', content: 'disposable probe'}]}));
  });
}
function record(name, details = {}) { report.checks.push({name, ...details}); console.log('PASS: ' + name); }
try {
  const crd = await readFile(process.env.AEP_AUTHORINO_CRD_PATH, 'utf8');
  assert.equal(createHash('sha256').update(crd.replace(/\r\n/g, '\n')).digest('hex'), crdHash);
  assert(!(await command(kind, ['get', 'clusters'])).split(/\s+/).includes(cluster));
  owned = true;
  console.log('Creating owned native projection cluster');
  await command(kind, ['create', 'cluster', '--name', cluster, '--kubeconfig', kubeconfig, '--wait', '120s', '--image',
    'kindest/node@sha256:3489c7674813ba5d8b1a9977baea8a6e553784dab7b84759d1014dbd78f7ebd5']);
  const refs = path.join(work, 'operator-images.txt');
  console.log('Building current Operator with pinned ko');
  await command(process.env.KO ?? 'go', [...(process.env.KO ? [] : ['run', 'github.com/google/ko@v0.18.0']),
    'build', '--platform', 'linux/amd64', '--image-refs', refs, './services/gateway-operator/cmd'],
  {commandEnv: {...env, KO_DOCKER_REPO: 'ko.local'}, timeout: 600_000});
  const operatorImage = (await readFile(refs, 'utf8')).trim();
  report.operatorImage = operatorImage;
  for (const [i, ref] of ['node:24-alpine', operatorImage].entries()) {
    const archive = path.join(work, 'image-' + i + '.tar');
    await command('docker', ['image', 'save', '--platform=linux/amd64', '-o', archive, ref]);
    await command(kind, ['load', 'image-archive', '--name', cluster, archive]);
    await rm(archive);
  }
  // Docker's single-platform export rewrites OCI indexes. Keep the original
  // pinned Authorino index and import its digest, avoiding dangling import IDs
  // and an unnecessary network pull in the disposable containerd node.
  const nativeArchive = path.join(work, 'authorino.tar');
  const nativeTag = image.split('@')[0];
  const nativeDigest = image.split('@')[1];
  const inspection = JSON.parse(await command('docker', ['image', 'inspect', nativeTag]));
  assert(inspection[0].RepoDigests.includes(nativeTag.slice(0, nativeTag.lastIndexOf(':')) + '@' + nativeDigest));
  await command('docker', ['image', 'save', '-o', nativeArchive, nativeTag]);
  await command('docker', ['cp', nativeArchive, cluster + '-control-plane:/aep-native-authorino.tar']);
  await command('docker', ['exec', cluster + '-control-plane', 'ctr', '-n', 'k8s.io', 'images', 'import',
    '--digests', '--platform', 'linux/amd64', '/aep-native-authorino.tar']);
  await command('docker', ['exec', cluster + '-control-plane', 'rm', '--', '/aep-native-authorino.tar']);
  await rm(nativeArchive);
  await kubectl(['apply', '-f', '-'], {input: crd});
  await kubectl(['apply', '-f', 'services/gateway-operator/config/default/namespaces.yaml']);
  await apply([{apiVersion: 'v1', kind: 'Namespace', metadata: {name: 'higress-system'}}]);
  await kubectl(['apply', '-f', 'services/gateway-operator/internal/controller/testdata/higress-crds.yaml']);
  let manifest = await command('kubectl', ['kustomize', 'services/gateway-operator/config/default']);
  manifest = manifest.replace('ko://github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/cmd', operatorImage);
  await kubectl(['apply', '-f', '-'], {input: manifest});
  await kubectl(['apply', '-k', 'services/gateway-operator/config/access-rbac']);
  const httpsSource = sourceDirectory ? await httpsPolicySource({directory: sourceDirectory, command, kubectl, apply, work, cluster, namespace}) : undefined;
  await kubectl(['-n', namespace, 'set', 'env', 'deployment/aep-gateway-operator', 'AEP_GATEWAY_ACCESS_OPERATOR_ENABLED=true',
    'AEP_GATEWAY_ACCESS_ISSUER_URL=http://introspection:8080/oidc', 'AEP_GATEWAY_ACCESS_POLICY_BASE_URL=' + (httpsSource?.baseURL ?? 'http://introspection:8082'),
    'AEP_GATEWAY_ACCESS_REFRESH_SECONDS=1', 'AEP_GATEWAY_ACCESS_REQUIRE_ENTITLEMENT=' + requireEntitlement]);
  const spec = {deploymentId: 'probe', subject: {kind: 'human', id: 'alice'}, binding: {kind: 'session', sessionId: 'session-a'},
    publication: {sequence: 1, operationId: '11111111-1111-1111-1111-111111111111', revision: 'native-one', contentHash: 'a'.repeat(64)},
    issuedAt: new Date(Math.floor(Date.now() / 1000) * 1000).toISOString(), validUntil: new Date(Date.now() + 600_000).toISOString(),
    disabled: false, models: ['model-a', 'model-b'], licenses: [
      {id: 'license-a', digest: 'sha256:' + 'a'.repeat(64)},
      {id: 'license-grace', digest: 'sha256:' + 'a'.repeat(64), validUntil: new Date(Date.now() + 600_000).toISOString()},
    ]};
  const binding = createHash('sha256').update(JSON.stringify({deploymentId: spec.deploymentId, subject: spec.subject, binding: spec.binding})).digest('hex');
  const name = 'access-' + binding;
  function grant() {
    return {apiVersion: 'gateway.aep.zhiyuan.io/v1alpha1', kind: 'ModelAccessGrant', spec,
      metadata: {name, namespace, labels: {'gateway.aep.zhiyuan.io/publication-deployment': createHash('sha256').update('probe').digest('hex').slice(0, 32)},
        annotations: {'gateway.aep.zhiyuan.io/access-binding': binding, 'gateway.aep.zhiyuan.io/publication-sequence': String(spec.publication.sequence),
          'gateway.aep.zhiyuan.io/publication-operation-id': spec.publication.operationId, 'gateway.aep.zhiyuan.io/publication-revision': spec.publication.revision,
          'gateway.aep.zhiyuan.io/publication-content-hash': spec.publication.contentHash}}};
  }
  await apply([{apiVersion: 'gateway.aep.zhiyuan.io/v1alpha1', kind: 'DeploymentConfig', metadata: {name: 'deployment', namespace}, spec: {deploymentId: 'probe'}}, grant()]);
  const mockSource = await readFile(path.join(root, 'tests/e2e/auth-cache/mock-introspection.mjs'), 'utf8');
  const policySource = await readFile(path.join(root, 'tests/e2e/auth-cache/native-policy-source.mjs'), 'utf8');
  const objects = JSON.parse(JSON.stringify(resources(mockSource + (httpsSource ? '' : '\n' + policySource))).replaceAll('auth-cache-probe', namespace));
  // freecache rejects individual entries larger than 1/1024 of its store.
  // Raw enterprise JWT keys plus claims exceed the upstream 1 MB default.
  // Four MB covers this fixture, NOT the maximum public scopes or fleet size.
  for (const object of objects.filter(o => o.kind === 'Deployment' && o.metadata.name.startsWith('authorino-'))) {
    const container = object.spec.template.spec.containers[0];
    container.args = container.args.map(arg => arg === '--evaluator-cache-size=1' ? '--evaluator-cache-size=4' : arg);
  }
  if (httpsSource) httpsSource.configure(objects);
  else {
    const mockDeployment = objects.find(o => o.kind === 'Deployment' && o.metadata.name === 'introspection');
    mockDeployment.spec.template.spec.volumes.push({name: 'policies', configMap: {name}});
    mockDeployment.spec.template.spec.containers[0].volumeMounts.push({name: 'policies', mountPath: '/policies', readOnly: true});
    objects.find(o => o.kind === 'Service' && o.metadata.name === 'introspection').spec.ports.push({name: 'policies', port: 8082});
    objects.find(o => o.kind === 'Service' && o.metadata.name === 'introspection').spec.ports[0].name = 'metadata';
  }
  await apply(objects);
  for (const name of ['aep-gateway-operator', 'introspection', 'authorino-a', 'authorino-b']) {
    console.log('Waiting for ' + name);
    await kubectl(['-n', namespace, 'rollout', 'status', 'deployment/' + name, '--timeout=150s']);
  }
  const ports = await Promise.all(['authorino-a', 'authorino-b'].map(name => forward('deployment/' + name, 5001)));
  const metadataPort = await forward('deployment/introspection', 8080);
  const fixture = JSON.parse(await command('go', ['run', './services/control-service/testdata/auth-cache-tokens'], {input: '{"productDiscovery":true}'}));
  const activeToken = requireEntitlement ? fixture.tokens.entitlement : fixture.tokens.model;
  const tokenFor = name => fixture.tokens[requireEntitlement && name !== 'access' ? 'entitlement-' + name : name];
  for (const [route, value] of [['jwks', fixture.jwks], ['metadata', fixture.discovery]]) {
    const response = await fetch('http://127.0.0.1:' + metadataPort + '/' + route, {method: 'POST', headers: {'content-type': 'application/json'},
      body: JSON.stringify(value), signal: AbortSignal.timeout(5000)});
    assert.equal(response.status, 200);
  }
  for (const port of ports) await until(async () => {
    const result = await request(port, binding + '.access.aep.invalid', activeToken);
    assert.equal(result.status, 200); assert.equal(result.observation.publication.sequence, 1);
  });
  record('Actual Operator projection is accepted by pinned native replicas using real AEP JWTs');
  if (httpsSource) {
    const untrustedPort = await forward('deployment/authorino-no-ca', 5001);
    await until(async () => {
      const result = await request(untrustedPort, binding + '.access.aep.invalid', activeToken);
      assert(result.status >= 400 && result.status < 600);
      const logs = await kubectl(['-n', namespace, 'logs', 'deployment/authorino-no-ca', '--tail=100']);
      assert(logs.includes('certificate signed by unknown authority'), 'Negative control must fail specifically on CA verification');
    });
    record('Native Authorino trusts the explicitly mounted CA; the otherwise identical cold process without it denies on x509 verification');
  }
  for (const port of ports) {
    for (const name of ['signature', 'access', 'issuer', 'audience', 'use', 'deployment', 'subject', 'session', 'missing-session', 'scope', 'password', 'expired', 'missing-exp', 'future']) {
      assert([401, 403].includes((await request(port, binding + '.access.aep.invalid', tokenFor(name))).status), 'Invalid token must deny: ' + name);
    }
    assert.equal((await request(port, binding + '.access.aep.invalid', activeToken, 'model-b')).status, 403);
    for (const body of ['{', 'null', '[]', '{}', '{"model":42}', '{"model":["model-a"]}']) {
      assert.equal((await request(port, binding + '.access.aep.invalid', activeToken, 'model-a', body)).status, 403);
    }
    for (const name of ['license-id', 'license-digest', 'license-missing']) {
      assert.equal((await request(port, binding + '.access.aep.invalid', fixture.tokens[name])).status, 403);
    }
    assert.equal((await request(port, binding + '.access.aep.invalid', fixture.tokens.entitlement)).status, 200);
    assert.equal((await request(port, binding + '.access.aep.invalid', fixture.tokens['license-grace'])).status, 200);
    assert.equal((await request(port, binding + '.access.aep.invalid', fixture.tokens.model)).status, requireEntitlement ? 403 : 200);
  }
  record('Fourteen invalid tokens and actual-body model escalation deny on both native replicas');
  record('Malformed JSON bodies and mismatched license ID/digest deny; exact perpetual and grace entitlements allow');
  const short = tokenFor('native-short');
  const expires = JSON.parse(Buffer.from(short.split('.')[1], 'base64url')).exp;
  assert(expires > Date.now() / 1000 + 1, 'Short-token setup exceeded validity');
  for (const port of ports) assert.equal((await request(port, binding + '.access.aep.invalid', short)).status, 200);
  await until(async () => assert(Date.now() / 1000 > expires + 0.1));
  for (const port of ports) assert.equal((await request(port, binding + '.access.aep.invalid', short)).status, 403);
  record('Warm credential results deny after JWT expiry on both native replicas');
  const initial = await get('authconfig', name);
  spec.publication.sequence++; spec.publication.revision = 'native-renewal';
  spec.validUntil = new Date(Date.now() + 660_000).toISOString();
  const started = Date.now(); await apply([grant()]);
  for (const port of ports) await until(async () => {
    const result = await request(port, binding + '.access.aep.invalid', activeToken);
    assert.equal(result.status, 200); assert.equal(result.observation.publication.sequence, 2);
  });
  assert.equal((await get('authconfig', name)).metadata.resourceVersion, initial.metadata.resourceVersion);
  record('CR renewal reaches both replicas through ' + (httpsSource ? 'native watch and HTTPS' : 'real ConfigMap file projection') + ' without rewriting AuthConfig', {propagationMs: Date.now() - started});
  spec.publication.sequence++; spec.publication.revision = 'native-license-expiry';
  spec.licenses[1].validUntil = new Date(Date.now() - 1000).toISOString();
  await apply([grant()]);
  for (const port of ports) await until(async () => {
    assert.equal((await request(port, binding + '.access.aep.invalid', fixture.tokens['license-grace'])).status, 403);
    assert.equal((await request(port, binding + '.access.aep.invalid', fixture.tokens.entitlement)).status, 200);
  });
  record('An expired grace license denies its warm entitlement without accepting a different active license as a substitute');
  if (httpsSource) await sourceFailureChecks({kubectl, apply, get, grant, spec, name, ports, binding,
    token: activeToken, request, until, record});
  spec.publication.sequence++; spec.publication.revision = 'native-revoke'; spec.publication.contentHash = 'b'.repeat(64);
  spec.disabled = true; spec.models = []; await apply([grant()]);
  for (const port of ports) await until(async () => assert.equal((await request(port, binding + '.access.aep.invalid', activeToken)).status, 403));
  const observed = await get('modelaccessgrant', name);
  assert.notEqual(observed.status?.conditions?.find(c => c.type === 'Ready')?.status, 'True');
  assert.equal(observed.status?.replicas?.length ?? 0, 0);
  record('Disabled CR revokes previously allowed JWTs on both replicas; resource status never fabricates all-process adoption');
  report.completed = true;
} catch (error) {
  report.completed = false; report.error = error.message;
  if (owned) {
    try {report.fixturePods = await kubectl(['-n', namespace, 'get', 'pods', '-o', 'wide'], {timeout: 10_000});}
    catch {report.fixturePods = 'Unavailable';}
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
    if (process.env.AEP_NATIVE_ACCESS_REPORT) await writeFile(process.env.AEP_NATIVE_ACCESS_REPORT, JSON.stringify(report, null, 2) + '\n');
    console.log(JSON.stringify(report, null, 2));
  }
}

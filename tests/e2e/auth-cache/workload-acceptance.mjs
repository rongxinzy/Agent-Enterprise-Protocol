// Isolated native TokenReview/OPA acceptance. No business DB, cloud credentials
// or employee runtime claims: a real Kubernetes Pod-bound JWT is the identity.
import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {readFile} from 'node:fs/promises';
import path from 'node:path';
import {resources} from './resources.mjs';

const namespace = 'aep-system';
const audience = 'zhiyuan-model-access';
const sha = value => createHash('sha256').update(value).digest('hex');

function accessGrant(account, uid, subject) {
  const spec = {deploymentId: 'probe', subject: {kind: 'agent', id: subject}, binding: {kind: 'kubernetes', workload: {
    namespace, serviceAccountName: account, serviceAccountUid: uid, employeeUid: 'employee-cr-' + subject, audience,
  }}, publication: {sequence: 1, operationId: '11111111-1111-1111-1111-111111111111', revision: 'workload-one', contentHash: 'a'.repeat(64)},
  issuedAt: new Date(Math.floor(Date.now() / 1000) * 1000).toISOString(), validUntil: new Date(Date.now() + 1800_000).toISOString(),
  disabled: false, models: ['model-a'], roleIds: ['employee-role'], teamIds: ['employee-team']};
  const binding = sha(JSON.stringify({deploymentId: spec.deploymentId, subject: spec.subject, binding: spec.binding}));
  return {binding, spec, grant() {
    return {apiVersion: 'gateway.aep.zhiyuan.io/v1alpha1', kind: 'ModelAccessGrant', spec,
      metadata: {name: 'access-' + binding, namespace, labels: {'gateway.aep.zhiyuan.io/publication-deployment': sha('probe').slice(0, 32)},
        annotations: {'gateway.aep.zhiyuan.io/access-binding': binding, 'gateway.aep.zhiyuan.io/publication-sequence': String(spec.publication.sequence),
          'gateway.aep.zhiyuan.io/publication-operation-id': spec.publication.operationId,
          'gateway.aep.zhiyuan.io/publication-revision': spec.publication.revision,
          'gateway.aep.zhiyuan.io/publication-content-hash': spec.publication.contentHash}}};
  }};
}

export async function workloadAcceptance({kubectl, apply, forward, until, request, get, record, root}) {
  assert.notEqual(process.env.AEP_NATIVE_ACCESS_REQUIRE_ENTITLEMENT, 'true', 'Workload entitlement contract is not implemented');
  assert(!process.env.AEP_NATIVE_ACCESS_POLICY_SOURCE_DIR, 'This probe uses only the disposable mounted file source');
  await kubectl(['-n', namespace, 'set', 'env', 'deployment/aep-gateway-operator', 'AEP_GATEWAY_ACCESS_OPERATOR_ENABLED=true',
    'AEP_GATEWAY_ACCESS_ISSUER_URL=http://introspection:8080/oidc', 'AEP_GATEWAY_ACCESS_POLICY_BASE_URL=http://introspection:8082',
    'AEP_GATEWAY_ACCESS_REFRESH_SECONDS=1', 'AEP_GATEWAY_ACCESS_REQUIRE_ENTITLEMENT=false',
    'AEP_GATEWAY_ACCESS_WORKLOAD_AUDIENCE=' + audience]);
  for (const name of ['employee-a', 'employee-b']) {
    await apply([{apiVersion: 'v1', kind: 'ServiceAccount', metadata: {name, namespace}, automountServiceAccountToken: false},
      {apiVersion: 'v1', kind: 'Pod', metadata: {name, namespace}, spec: {serviceAccountName: name, automountServiceAccountToken: false,
        securityContext: {runAsNonRoot: true, runAsUser: 1000, seccompProfile: {type: 'RuntimeDefault'}},
        containers: [{name: 'fixture', image: 'node:24-alpine', command: ['node', '-e', 'setInterval(()=>{},60000)'],
          securityContext: {allowPrivilegeEscalation: false, capabilities: {drop: ['ALL']}}}]}}]);
    await kubectl(['-n', namespace, 'wait', '--for=condition=Ready', 'pod/' + name, '--timeout=120s']);
  }
  const a = accessGrant('employee-a', (await get('serviceaccount', 'employee-a')).metadata.uid, 'agent-a');
  const b = accessGrant('employee-b', (await get('serviceaccount', 'employee-b')).metadata.uid, 'agent-b');
  const stale = accessGrant('employee-a', 'replaced-service-account-uid', 'agent-a');
  const grants = [a, b, stale];
  for (const g of grants) {
    g.spec.workloadPodUids = [(await get('pod', g.spec.binding.workload.serviceAccountName)).metadata.uid];
  }
  await apply([{apiVersion: 'gateway.aep.zhiyuan.io/v1alpha1', kind: 'DeploymentConfig', metadata: {name: 'deployment', namespace},
    spec: {deploymentId: 'probe'}}, ...grants.map(g => g.grant())]);
  for (const g of grants) await until(() => get('configmap', 'access-' + g.binding));
  const mock = await readFile(path.join(root, 'tests/e2e/auth-cache/mock-introspection.mjs'), 'utf8');
  const source = await readFile(path.join(root, 'tests/e2e/auth-cache/native-policy-source.mjs'), 'utf8');
  const objects = JSON.parse(JSON.stringify(resources(mock + '\n' + source)).replaceAll('auth-cache-probe', namespace));
  for (const o of objects.filter(o => o.kind === 'Deployment')) {
    o.spec.template.spec.securityContext = {runAsNonRoot: true, runAsUser: 1000, seccompProfile: {type: 'RuntimeDefault'}};
  }
  // Upstream freecache limits one entry to 1/1024 of its store. A bound SA
  // bearer plus TokenReview status must fit, otherwise this is a cold-only test.
  for (const o of objects.filter(o => o.kind === 'Deployment' && o.metadata.name.startsWith('authorino-'))) {
    const c = o.spec.template.spec.containers[0];
    c.args = c.args.map(a => a === '--evaluator-cache-size=1' ? '--evaluator-cache-size=4' : a);
  }
  const server = objects.find(o => o.kind === 'Deployment' && o.metadata.name === 'introspection');
  server.spec.template.spec.volumes.push({name: 'policies', projected: {sources: grants.map(g => ({configMap: {name: 'access-' + g.binding}}))}});
  server.spec.template.spec.containers[0].volumeMounts.push({name: 'policies', mountPath: '/policies', readOnly: true});
  objects.find(o => o.kind === 'Service' && o.metadata.name === 'introspection').spec.ports = [{name: 'metadata', port: 8080}, {name: 'policies', port: 8082}];
  await apply(objects);
  for (const name of ['aep-gateway-operator', 'introspection', 'authorino-a', 'authorino-b']) {
    await kubectl(['-n', namespace, 'rollout', 'status', 'deployment/' + name, '--timeout=150s']);
  }
  const ports = await Promise.all(['authorino-a', 'authorino-b'].map(name => forward('deployment/' + name, 5001)));
  const token = async (name, requestedAudience = audience, bound = true) => kubectl(['-n', namespace, 'create', 'token', name,
    '--audience=' + requestedAudience, '--duration=10m', ...(bound ? ['--bound-object-kind=Pod', '--bound-object-name=' + name,
      '--bound-object-uid=' + (await get('pod', name)).metadata.uid] : [])]);
  const active = await token('employee-a');
  const peer = await token('employee-b');
  const wrongAudience = await token('employee-a', 'another-service');
  const unbound = await token('employee-a', audience, false);
  const call = (port, g = a, credential = active, model = 'model-a', body) => request(port, g.binding + '.access.aep.invalid', credential, model, body);
  // Cold negative control before the dedicated, review-only RBAC is enabled.
  for (const port of ports) await until(async () => assert([401, 403, 500].includes((await call(port)).status)));
  await kubectl(['apply', '-k', 'deploy/kubernetes/access-workload-review']);
  for (const port of ports) await until(async () => {
    const r = await call(port); assert.equal(r.status, 200); assert.equal(r.observation.publication.sequence, 1);
  });
  record('Both native replicas authenticate real Pod-bound SA tokens after explicit TokenReview RBAC; no AEP DB lookup');
  const reviewCount = async () => (await kubectl(['get', '--raw', '/metrics'])).split('\n')
    // Non-persisted review endpoints are reported as POST on some API servers.
    .filter(line => line.startsWith('apiserver_request_total{') && line.includes('resource="tokenreviews"') && /verb="(?:CREATE|POST)"/.test(line))
    .reduce((total, line) => total + Number(line.slice(line.lastIndexOf(' ') + 1)), 0);
  const reviewed = await reviewCount();
  assert(reviewed >= 2, 'API server metrics must witness the native TokenReview requests');
  for (const port of ports) for (let i = 0; i < 3; i++) assert.equal((await call(port)).status, 200);
  assert.equal(await reviewCount(), reviewed, 'Warm calls unexpectedly reissued TokenReview');
  record('API server metrics prove repeated warm calls issue zero further TokenReviews on both replicas');
  const expires = JSON.parse(Buffer.from(active.split('.')[1], 'base64url')).exp;
  assert(expires > Date.now() / 1000 + 60, 'Minimum TokenRequest lifetime should remain available for expiry test');
  const tampered = active.split('.');
  tampered[1] = Buffer.from(JSON.stringify({...JSON.parse(Buffer.from(tampered[1], 'base64url')), exp: expires + 600})).toString('base64url');
  for (const port of ports) {
    assert.equal((await call(port, b, peer)).status, 200);
    assert.equal((await call(port, b)).status, 403);
    assert.equal((await call(port, a, peer)).status, 403);
    assert.equal((await call(port, stale)).status, 403);
    for (const credential of [wrongAudience, unbound, tampered.join('.'), 'invalid']) {
      assert([401, 403].includes((await call(port, a, credential)).status));
    }
    assert.equal((await call(port, a, active, 'model-b')).status, 403);
    for (const body of ['{', 'null', '[]', '{}', '{"model":42}']) assert.equal((await call(port, a, active, 'model-a', body)).status, 403);
  }
  record('Cross-employee identity, stale SA UID, wrong audience, unbound/tampered tokens and actual-body escalation deny');
  const initial = await get('authconfig', 'access-' + a.binding);
  a.spec.publication.sequence++; a.spec.publication.revision = 'workload-renewed';
  a.spec.validUntil = new Date(Date.now() + 1900_000).toISOString(); await apply([a.grant()]);
  for (const port of ports) await until(async () => {
    const r = await call(port); assert.equal(r.status, 200); assert.equal(r.observation.publication.sequence, 2);
  });
  assert.equal((await get('authconfig', 'access-' + a.binding)).metadata.resourceVersion, initial.metadata.resourceVersion);
  record('Workload renewal reaches both replicas without rewriting AuthConfig');
  const observedPods = a.spec.workloadPodUids;
  const beforePodChange = await reviewCount();
  for (const pods of [b.spec.workloadPodUids, []]) {
    a.spec.publication.sequence++; a.spec.publication.revision = 'pod-observation-' + a.spec.publication.sequence;
    a.spec.workloadPodUids = pods; await apply([a.grant()]);
    for (const port of ports) await until(async () => {
      assert.equal((await call(port)).status, 403); assert.equal((await call(port, b, peer)).status, 200);
    });
    assert.equal((await get('authconfig', 'access-' + a.binding)).metadata.resourceVersion, initial.metadata.resourceVersion);
  }
  a.spec.publication.sequence++; a.spec.publication.revision = 'pod-observation-restored';
  a.spec.workloadPodUids = observedPods; await apply([a.grant()]);
  for (const port of ports) await until(async () => assert.equal((await call(port)).status, 200));
  assert.equal(await reviewCount(), beforePodChange, 'Pod membership updates evicted warm identities');
  record('Both warm replicas deny foreign/missing live Pod evidence and restore exact membership without TokenReview or peer eviction');
  a.spec.publication.sequence++; a.spec.publication.revision = 'model-revoked'; a.spec.models = [];
  await apply([a.grant()]);
  for (const port of ports) await until(async () => {
    assert.equal((await call(port)).status, 403); assert.equal((await call(port, b, peer)).status, 200);
  });
  a.spec.publication.sequence++; a.spec.publication.revision = 'model-restored'; a.spec.models = ['model-a']; await apply([a.grant()]);
  for (const port of ports) await until(async () => assert.equal((await call(port)).status, 200));
  record('Affected employee loses model access while the unrelated employee continues; restoring the grant allows again');
  a.spec.publication.sequence++; a.spec.publication.revision = 'short-publication';
  a.spec.validUntil = new Date(Date.now() + 8000).toISOString(); await apply([a.grant()]);
  for (const port of ports) await until(async () => assert.equal((await call(port)).status, 403));
  a.spec.publication.sequence++; a.spec.publication.revision = 'fresh-publication';
  a.spec.validUntil = new Date(Date.now() + 1900_000).toISOString(); await apply([a.grant()]);
  for (const port of ports) await until(async () => assert.equal((await call(port)).status, 200));
  record('Expired publication denies warm workload access; a newer live publication restores it');
  const rotated = await token('employee-a');
  assert(rotated !== active, 'The token rotation probe must use a different signed token');
  for (const port of ports) assert.equal((await call(port, a, rotated)).status, 200);
  record('Rotated Pod-bound token uses the stable workload binding with a new native authentication entry');
  let current = rotated;
  if (process.env.AEP_NATIVE_ACCESS_WORKLOAD_EXPIRY === 'true') {
    const beforeExpiry = await reviewCount();
    console.log('Waiting for actual ten-minute Kubernetes token expiry; no clock or token mutation');
    await until(async () => assert(Date.now() / 1000 > expires + 0.2), Math.max(1000, (expires * 1000 - Date.now()) + 10_000));
    for (const port of ports) assert.equal((await call(port)).status, 403);
    assert.equal(await reviewCount(), beforeExpiry, 'Expiry must deny the warmed result without relying on a cold TokenReview');
    record('Both previously warmed native replicas reject the original real Kubernetes token after exp', {tokenLifetimeSeconds: 600});
    const fresh = await token('employee-a');
    for (const port of ports) assert.equal((await call(port, a, fresh)).status, 200);
    current = fresh;
    record('Newly issued valid token still works under the same live policy after the previous token expires');
  }
  a.spec.publication.sequence++; a.spec.publication.revision = 'workload-retired'; a.spec.disabled = true; a.spec.models = [];
  await apply([a.grant()]);
  for (const port of ports) await until(async () => assert.equal((await call(port, a, current)).status, 403));
  assert.notEqual((await get('modelaccessgrant', 'access-' + a.binding)).status?.conditions?.find(c => c.type === 'Ready')?.status, 'True');
  record('Retired workload denies and no controller status fabricates complete adoption');
}

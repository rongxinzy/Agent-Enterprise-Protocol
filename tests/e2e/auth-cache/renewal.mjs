// Native external-policy refresh experiment; never a product policy server/cache.
import assert from 'node:assert/strict';
import {namespace, policy} from './resources.mjs';

const now = () => Math.floor(Date.now() / 1000);
const identityDecision = result => ({identity: result.identity, decision: result.decision});

function accessPolicy(subject, hash) {
  const name = 'renew-' + subject;
  const object = policy(name, {subject});
  object.spec.hosts = [name + '.probe.invalid'];
  object.spec.authorization.access = {
    opa: {rego: `allow {
input.auth.identity.deployment_id == "probe"
input.auth.identity.sub == ${JSON.stringify(subject)}
input.auth.identity.sid == ${JSON.stringify(subject === 'alice' ? 'session-a' : 'session-b')}
}
models = ["model-a", "model-b"]
policy_hash = ${JSON.stringify(hash)}
stamp = time.now_ns()`, allValues: true},
    cache: {key: {selector: 'context.request.http.headers.authorization'}, ttl: 300},
  };
  object.spec.authorization.bounds = {priority: 1, opa: {externalPolicy: {
    url: 'http://introspection:8080/policies/' + subject + '.rego', ttl: 1,
  }, allValues: true}};
  Object.assign(object.spec.response.success.headers['x-probe-witness'].json.properties, {
    publication: {selector: 'auth.authorization.bounds.publication'},
    validUntil: {selector: 'auth.authorization.bounds.valid_until'},
    hash: {selector: 'auth.authorization.access.policy_hash'},
  });
  return object;
}

function bounds(hash, publication, validUntil) {
  return `allow {
input.auth.authorization.access.policy_hash == ${JSON.stringify(hash)}
input.auth.identity.exp > time.now_ns() / 1000000000
${validUntil} > time.now_ns() / 1000000000
model := input.context.request.http.headers["x-aep-model-id"]
input.auth.authorization.access.models[_] == model
input.auth.identity.model_scopes[_] == model
}
publication = ${JSON.stringify(publication)}
valid_until = ${validUntil}`;
}

export async function renewal({publish, request, ports, statsPort, stats, kubectl, record, until}) {
  const upload = async (subject, rego, status = 200) => {
    const response = await fetch('http://127.0.0.1:' + statsPort + '/policies/' + subject + '.rego', {
      method: 'POST', headers: {'content-type': 'application/json'}, body: JSON.stringify({rego, status}),
      signal: AbortSignal.timeout(5000),
    });
    assert.equal(response.status, 200);
  };
  const call = (port, subject, model = 'model-a', token = 'probe-' + subject + '-session') =>
    request(port, 'renew-' + subject + '.probe.invalid', token, model);
  const allowed = (port, subject, publication, hash = subject + '-1') => until(async () => {
    const result = await call(port, subject);
    assert.equal(result.status, 200);
    assert.equal(result.publication, publication);
    assert.equal(result.hash, hash);
    assert(result.identity && result.decision && result.validUntil);
    return result;
  });
  const denied = async subject => {
    for (const port of ports) await until(async () => assert.equal((await call(port, subject)).status, 403));
  };
  const config = async subject => JSON.parse(await kubectl(['-n', namespace, 'get',
    'authconfig/renew-' + subject, '-o', 'json']));

  const originalDeadline = now() + 25;
  await upload('alice', bounds('alice-1', '1', originalDeadline));
  await upload('bob', bounds('bob-1', '1', now() + 180));
  await publish([accessPolicy('alice', 'alice-1'), accessPolicy('bob', 'bob-1')]);
  const warm = {alice: [], bob: []};
  for (const subject of ['alice', 'bob']) {
    for (const port of ports) warm[subject].push(identityDecision(await allowed(port, subject, '1')));
  }
  assert(now() + 3 < originalDeadline, 'Initial freshness elapsed before warming; no renewal evidence');
  const initialConfigs = {alice: await config('alice'), bob: await config('bob')};
  const before = await stats();
  const renewedDeadline = originalDeadline + 20;
  await upload('alice', bounds('alice-1', '2', renewedDeadline));
  for (const [i, port] of ports.entries()) {
    assert.deepEqual(identityDecision(await allowed(port, 'alice', '2')), warm.alice[i]);
    assert.deepEqual(identityDecision(await allowed(port, 'bob', '1')), warm.bob[i]);
    const otherModel = await call(port, 'alice', 'model-b');
    assert.equal(otherModel.status, 200);
    assert.deepEqual(identityDecision(otherModel), warm.alice[i]);
    assert.equal((await call(port, 'alice', 'model-c')).status, 403);
    assert.equal((await call(port, 'alice', 'model-a', 'probe-bob-session')).status, 403);
  }
  await until(async () => assert(now() > originalDeadline));
  for (const [i, port] of ports.entries()) {
    assert.deepEqual(identityDecision(await allowed(port, 'alice', '2')), warm.alice[i]);
  }
  const renewed = await stats();
  assert.equal(renewed.calls['session-a'], before.calls['session-a']);
  // The deliberate Bob-credential attack on Alice's config makes one cold miss per replica.
  assert.equal(renewed.calls['session-b'] - before.calls['session-b'], ports.length);
  for (const subject of ['alice', 'bob']) {
    const current = await config(subject);
    assert.deepEqual(current.spec, initialConfigs[subject].spec);
    assert.equal(current.metadata.resourceVersion, initialConfigs[subject].metadata.resourceVersion);
  }
  record('Native external-policy renewal advances on both replicas past original expiry without rebuilding AuthConfigs or warm identity/decisions',
    {replicas: ports.length, refreshSeconds: 1, originalDeadline, renewedDeadline});

  // A new freshness document cannot bless an old semantic policy during propagation.
  await upload('alice', bounds('alice-2', '3', renewedDeadline));
  await denied('alice');
  for (const [i, port] of ports.entries()) assert.deepEqual(identityDecision(await allowed(port, 'bob', '1')), warm.bob[i]);
  const beforeChange = await stats();
  await publish([accessPolicy('alice', 'alice-2')]);
  for (const [i, port] of ports.entries()) {
    const changed = identityDecision(await allowed(port, 'alice', '3', 'alice-2'));
    assert.notEqual(changed.identity, warm.alice[i].identity);
    assert.notEqual(changed.decision, warm.alice[i].decision);
    warm.alice[i] = changed;
    assert.deepEqual(identityDecision(await allowed(port, 'bob', '1')), warm.bob[i]);
  }
  assert.equal((await stats()).calls['session-a'] - beforeChange.calls['session-a'], ports.length);
  record('Freshness hash mismatch denies old cached policy; semantic Alice publication re-evaluates Alice only and keeps Bob warm');

  // Upstream retains the last compiled policy on download/compile failure. Its
  // absolute deadline must remain effective even with both allow caches warm.
  for (const [mode, document, status] of [['http-error', 'unavailable', 503], ['invalid-rego', 'allow {', 200]]) {
    const deadline = now() + 6;
    const publication = mode + '-valid';
    await upload('alice', bounds('alice-2', publication, deadline));
    for (const port of ports) await allowed(port, 'alice', publication, 'alice-2');
    const reads = (await stats()).policyReads['/policies/alice.rego'];
    await upload('alice', document, status);
    await until(async () => assert((await stats()).policyReads['/policies/alice.rego'] >= reads + ports.length * 2));
    await until(async () => assert(now() > deadline));
    await denied('alice');
    for (const [i, port] of ports.entries()) assert.deepEqual(identityDecision(await allowed(port, 'bob', '1')), warm.bob[i]);
    const beforeRecovery = await stats();
    await upload('alice', bounds('alice-2', mode + '-recovered', now() + 15));
    for (const [i, port] of ports.entries()) {
      assert.deepEqual(identityDecision(await allowed(port, 'alice', mode + '-recovered', 'alice-2')), warm.alice[i]);
    }
    assert.equal((await stats()).calls['session-a'], beforeRecovery.calls['session-a']);
    record('Native refresher ' + mode + ' cannot extend expired allow; same-policy recovery retains both warm results on both replicas');
  }
}

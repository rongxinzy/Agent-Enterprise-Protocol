// Native component compatibility evidence, not a production OIDC adapter.
import assert from 'node:assert/strict';
import {policy} from './resources.mjs';

function jwtPolicy({discovery = false, revision = '1', sessions = ['session-a'], enabled = true} = {}) {
  const object = policy('jwt-alice', {subject: 'alice', revision});
  object.spec.hosts = ['jwt-alice.probe.invalid'];
  object.spec.authentication.session = {
    jwt: discovery ? {issuerUrl: 'http://introspection:8080/oidc'} : {jwksUrl: 'http://introspection:8080/jwks'},
    cache: {key: {selector: 'context.request.http.headers.authorization'}, ttl: 300},
  };
  object.spec.authorization.access = {
    opa: {rego: `allow {
input.auth.identity.iss == "http://introspection:8080/oidc"
input.auth.identity.aud[_] == "model-gateway"
input.auth.identity.token_use == "model"
input.auth.identity.deployment_id == "probe"
input.auth.identity.sub == "alice"
not input.auth.identity.password_change_required
${JSON.stringify(sessions)}[_] == input.auth.identity.session_id
${enabled}
}
models = ["model-a"]
stamp = time.now_ns()`, allValues: true},
    cache: {key: {selector: 'context.request.http.headers.authorization'}, ttl: 300},
  };
  object.spec.authorization.bounds = {priority: 1, opa: {rego: `allow {
input.auth.identity.exp > time.now_ns() / 1000000000
object.get(input.auth.identity, "nbf", 0) <= time.now_ns() / 1000000000
model := input.context.request.http.headers["x-aep-model-id"]
input.auth.authorization.access.models[_] == model
input.auth.identity.model_scopes[_] == model
}`}};
  // JWT jti is a binding witness, not evidence that signature evaluation was skipped.
  object.spec.response.success.headers['x-probe-witness'].json.properties.identity = {selector: 'auth.identity.jti'};
  return object;
}

export async function aepJwt({command, publish, request, ports, statsPort, record, until}) {
  // The actual AEP issuer generates keys in memory. Capture, never log its output.
  const fixture = JSON.parse(await command('go', ['run', './services/control-service/testdata/auth-cache-tokens']));
  const upload = await fetch('http://127.0.0.1:' + statsPort + '/jwks', {method: 'POST',
    headers: {'content-type': 'application/json'}, body: JSON.stringify(fixture.jwks), signal: AbortSignal.timeout(5000)});
  assert.equal(upload.status, 200);
  const call = (port, name = 'model', model = 'model-a') => request(port, 'jwt-alice.probe.invalid', fixture.tokens[name], model);
  await publish([jwtPolicy()]);
  for (const port of ports) assert.equal((await call(port)).status, 401);
  record('Known limitation: direct JWKS mode rejects actual AEP EdDSA model tokens', {targetSatisfied: false});

  await publish([jwtPolicy({discovery: true, revision: '2'})]);
  const warm = [];
  for (const port of ports) {
    const first = await until(async () => {
      const result = await call(port);
      assert.equal(result.status, 200); assert.equal(result.revision, '2');
      assert(result.identity && result.decision);
      return result;
    });
    warm.push(first);
    for (let i = 0; i < 3; i++) assert.deepEqual(await call(port), first);
  }
  record('Discovery declaring EdDSA accepts actual AEP model JWTs and reuses full allowed-range decisions on both replicas');
  const negatives = ['signature', 'access', 'issuer', 'audience', 'use', 'deployment', 'subject', 'session',
    'missing-session', 'scope', 'password', 'expired', 'missing-exp', 'future'];
  for (const port of ports) {
    for (const name of negatives) {
      const status = (await call(port, name)).status;
      assert([401, 403].includes(status), 'JWT boundary must deny: ' + name + ' (status ' + status + ')');
    }
    assert.equal((await call(port, 'model', 'model-b')).status, 403);
    assert.deepEqual(await call(port), warm[ports.indexOf(port)]);
  }
  record('Both replicas deny fourteen invalid JWT cases and out-of-range requests without disturbing valid warm decisions',
    {negativeTokenCases: negatives.length, outOfRangeRequest: true});

  // Require a successful warm call before expiry; a denied expired token alone
  // would not establish that the uncached guard also protects a cached identity.
  const shortExp = JSON.parse(Buffer.from(fixture.tokens.short.split('.')[1], 'base64url')).exp;
  assert(shortExp > Date.now() / 1000 + 1, 'JWT setup exceeded short-token validity; no expiry evidence collected');
  for (const port of ports) assert.equal((await call(port, 'short')).status, 200);
  await until(async () => assert(Date.now() / 1000 > shortExp + 0.2));
  for (const port of ports) assert.equal((await call(port, 'short')).status, 403);
  record('Uncached validity guard denies expired real JWTs even when identity and decision are warm');

  await publish([jwtPolicy({discovery: true, revision: '3', sessions: []})]);
  for (const port of ports) await until(async () => assert.equal((await call(port)).status, 403));
  record('Published session withdrawal denies previously warm real AEP model tokens on both replicas');
  await publish([jwtPolicy({discovery: true, revision: '4', enabled: false})]);
  for (const port of ports) await until(async () => assert.equal((await call(port)).status, 403));
  record('Disabled-principal projection remains deny with a cryptographically valid model JWT');
}

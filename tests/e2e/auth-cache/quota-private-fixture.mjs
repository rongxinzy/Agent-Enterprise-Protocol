// Shared production renderer -> native Istio/Higress; runtime wiring is separate.
// The original management probe/result remains unchanged and adoption stays off.
import assert from 'node:assert/strict';
import http from 'node:http';

// Node's fetch does not preserve an explicit Host on this runtime. Native
// domain-scoped routing requires the actual service Host, including when the
// transport connects through an ephemeral local port-forward.
function managementRequest(port, path, {method, headers, body}) {
  return new Promise((resolve, reject) => {
    const request = http.request({host: '127.0.0.1', port, path, method, headers, agent: false, timeout: 8000}, response => {
      let text = '';
      response.on('data', chunk => text += chunk);
      response.on('end', () => resolve({status: response.statusCode, text}));
      response.on('error', reject);
    });
    request.on('error', reject);
    request.on('timeout', () => request.destroy(new Error('Private management request timeout')));
    request.end(body);
  });
}

export async function privateQuotaFixture(ctx, {quota, credential, configureQuota}) {
  const {command, kubectl, apply, forward, until, gateway, person, record, report} = ctx;
  const list = JSON.parse(await kubectl(['-n', 'higress-system', 'get', 'wasmplugins', '-o', 'json'])).items;
  const source = list.find(p => p.metadata.name === quota.name);
  const auth = list.find(p => p.metadata.name.startsWith('aep-quota-auth-'));
  assert(source && auth, 'Existing native quota/key-auth configuration is required');
  const redis = source.spec.matchRules[0].config.redis;
  const rendered = JSON.parse(await command('go', ['run', './services/gateway-reconciler/internal/reconciler/testdata/render-native'], {
    input: JSON.stringify({desired: {deploymentId: 'quota-private-fixture'}, privateQuota: true, adminCredential: credential,
      native: {Enabled: true, RedisService: redis.service_name, RedisPort: redis.service_port,
        RedisDatabase: redis.database, RedisUsername: redis.username ?? ''}}),
  }));
  const objects = rendered.map(resource => JSON.parse(resource.Body));
  const service = objects.find(object => object.kind === 'Service');
  const name = service.metadata.name;
  const host = objects.find(object => object.kind === 'Gateway').spec.servers[0].hosts[0];
  const port = service.spec.ports[0].port;
  const managementPath = objects.find(object => object.kind === 'VirtualService').spec.http[0].match[0].uri.prefix;
  const privateAuth = objects.find(object => object.kind === 'WasmPlugin' && object.spec.phase === 'AUTHN');
  const privateQuota = objects.find(object => object.kind === 'WasmPlugin' && object.spec.phase === 'UNSPECIFIED_PHASE');
  // Native policies for another deployment's management host must not deny
  // this deployment merely because both use the same private gateway port.
  const peerGuard = structuredClone(objects.find(object => object.kind === 'AuthorizationPolicy'));
  peerGuard.metadata.name = name + '-peer-guard';
  peerGuard.spec.rules[0].to[0].operation.hosts = ['peer-' + host, 'peer-' + host + ':*'];
  peerGuard.spec.rules[0].when[0].notValues = ['Bearer disposable-peer-admin'];
  objects.push(peerGuard);
  const findings = {completed: false, productRendererImplemented: true, operatorPrivateRuntimeIntegrated: false,
    networkPolicyEnforcementVerified: false};
  report.privateQuotaFixture = findings;
  const created = [];
  let primaryFailure = false;
  try {
    for (const object of objects) {
      await apply([object]);
      created.push(object);
    }
    const {adminPort} = gateway.nativeRedis;
    await until(async () => {
      const dump = await (await fetch('http://127.0.0.1:' + adminPort + '/config_dump')).json();
      const listeners = dump.configs.flatMap(c => c.dynamic_listeners ?? []).map(l => l.active_state?.listener).filter(Boolean);
      const listener = listeners.find(l => l.address?.socket_address?.port_value === port);
      assert(listener, 'Private native listener has not loaded');
      const filters = listener.filter_chains.flatMap(c => c.filters ?? []).flatMap(f => f.typed_config?.http_filters ?? []);
      assert(!filters.some(f => f.name === 'envoy.filters.http.ext_authz'), 'Private capability fixture unexpectedly shares model authentication');
      const rbac = filters.find(f => f.name === 'envoy.filters.http.rbac');
      assert(rbac && JSON.stringify(rbac.typed_config).includes(name + '-guard'), 'Native private-port DENY policy is not loaded');
      assert(JSON.stringify(rbac.typed_config).includes(name + '-peer-guard'), 'Peer management host policy is not loaded');
      const loaded = dump.configs.flatMap(c => c.ecds_filters ?? []).map(e => e.ecds_filter?.name ?? '');
      for (const plugin of [privateAuth, privateQuota]) assert(loaded.some(n => n.endsWith('.' + plugin.metadata.name)), 'Private native plugin is not loaded');
    });
    const local = await forward('deployment/higress-gateway', port, 'higress-system');
    const call = async (suffix = '?consumer=probe', {token = credential, method = 'GET', body, headers = {}, path = managementPath} = {}) => {
      return managementRequest(local, path + suffix, {
        method, headers: {host, authorization: 'Bearer ' + token, 'content-type': 'application/x-www-form-urlencoded', ...headers},
        body});
    };
    await until(async () => assert.equal((await call()).status, 200, 'Native management key did not query the private fixture'));
    assert.equal((await call('/refresh', {method: 'POST', body: 'consumer=probe&quota=10'})).status, 200);
    const probeBalance = async () => {
      const response = await call();
      assert.equal(response.status, 200);
      return JSON.parse(response.text).quota;
    };
    assert.equal(await probeBalance(), 10);
    record('Native Gateway/VirtualService and domain-scoped ai-quota/key-auth serve the private fixture');
    const before = await gateway.stats();
    findings.denied = [];
    for (const [label, token, headers] of [
      ['no key', '', {}], ['wrong key', 'invalid-disposable-key', {}],
      ['model JWT', person.tokens.modelAccessToken, {}],
      ['forged admin', '', {'x-mse-consumer': name, 'x-consumer-id': name}],
    ]) {
      for (const suffix of ['?consumer=probe', '/refresh']) {
        const result = await call(suffix, {token, headers, ...(suffix.startsWith('/refresh')
          ? {method: 'POST', body: 'consumer=probe&quota=99'} : {})});
        findings.denied.push({label, operation: suffix.startsWith('/refresh') ? 'refresh' : 'read', status: result.status});
        assert(result.status >= 400 && result.status < 500, 'Untrusted identity reached native quota management');
      }
    }
    assert.equal(await probeBalance(), 10, 'Rejected management traffic changed the native balance');
    assert.equal(await gateway.stats(), before);
    record('Private fixture native key-auth rejects missing/wrong/model credentials and forged admin headers');
    // ai-quota can handle a request before router-level matching. The native
    // DENY policy must protect management even if the key-auth plugin is absent.
    await kubectl(['-n', 'higress-system', 'delete', 'wasmplugin', privateAuth.metadata.name]);
    await until(async () => {
      const dump = await (await fetch('http://127.0.0.1:' + adminPort + '/config_dump')).json();
      const listener = dump.configs.flatMap(c => c.dynamic_listeners ?? [])
        .map(l => l.active_state?.listener).find(l => l?.address?.socket_address?.port_value === port);
      assert(listener, 'Private listener disappeared while removing the guard');
      const filters = listener.filter_chains.flatMap(c => c.filters ?? []).flatMap(f => f.typed_config?.http_filters ?? []);
      assert(!filters.some(f => f.name?.endsWith('.' + privateAuth.metadata.name)), 'Removed guard remains in the active private filter chain');
      assert(filters.some(f => f.name?.endsWith('.' + privateQuota.metadata.name)), 'Quota extension disappeared with the authentication guard');
      // Envoy can retain an unused ECDS config for a draining old listener;
      // current enforcement is defined by the active listener's references.
    });
    const withoutGuard = await call('/refresh', {method: 'POST', token: 'wrong-key', body: 'consumer=probe&quota=99',
      headers: {'x-mse-consumer': name}});
    findings.missingGuardStatus = withoutGuard.status;
    assert(withoutGuard.status >= 400 && withoutGuard.status < 500, 'Missing key-auth allowed forged consumer administration');
    findings.missingGuardHosts = [];
    for (const authority of [host + ':' + port, host.toUpperCase(), host.toUpperCase() + ':' + port]) {
      const response = await call('/refresh', {method: 'POST', token: 'wrong-key', body: 'consumer=probe&quota=99',
        headers: {host: authority, 'x-mse-consumer': name}});
      findings.missingGuardHosts.push({authority, status: response.status});
      assert(response.status >= 400 && response.status < 500, 'Native management policy allowed a forged credential with an alternate authority');
    }
    findings.missingGuardDenied = [];
    for (const [label, token] of [['no key', ''], ['model JWT', person.tokens.modelAccessToken]]) {
      const response = await call('/delta', {method: 'POST', token, body: 'consumer=probe&value=99',
        headers: {'x-mse-consumer': name, 'x-consumer-id': name}});
      findings.missingGuardDenied.push({label, status: response.status});
      assert(response.status >= 400 && response.status < 500, 'Native management policy allowed an untrusted delta without key-auth');
    }
    await apply([privateAuth]);
    await until(async () => assert.equal((await call()).status, 200));
    assert.equal(await probeBalance(), 10, 'Missing key-auth allowed a rejected request to mutate native quota');
    record('Private fixture keeps management denied when native key-auth disappears and recovers after restoration');
    const consumer = 'aep.' + Buffer.from('probe').toString('base64url') + '.' + Buffer.from(person.user.id).toString('base64url');
    const query = async () => {
      const response = await call('?consumer=' + encodeURIComponent(consumer));
      assert.equal(response.status, 200);
      return JSON.parse(response.text);
    };
    assert.equal((await call('/refresh', {method: 'POST', body: new URLSearchParams({consumer, quota: '10'}).toString()})).status, 200);
    assert.equal((await query()).quota, 10);
    assert.equal((await call('/delta', {method: 'POST', body: new URLSearchParams({consumer, value: '-3'}).toString()})).status, 200);
    assert.equal((await query()).quota, 7);
    record('Native private fixture GET/refresh/delta read Redis balances without AEP arithmetic');
    const admin = await configureQuota('http://' + host + ':' + port + managementPath);
    assert.equal((await admin.getGatewayQuota(person.user.id)).quota, 7);
    assert.equal((await admin.refreshGatewayQuota(person.user.id, 10)).quota, 10);
    assert.equal((await admin.changeGatewayQuota(person.user.id, -3)).quota, 7);
    record('Real AEP quota GET/refresh/delta reach the native private fixture');
    assert.equal((await gateway.call(person)).status, 200);
    await until(async () => assert.equal((await admin.getGatewayQuota(person.user.id)).quota, 5));
    await admin.refreshGatewayQuota(person.user.id, 0);
    const limitedBefore = await gateway.stats();
    assert.equal((await gateway.call(person)).status, 403);
    assert.equal(await gateway.stats(), limitedBefore);
    record('Existing managed model route deducts native usage and rejects zero quota before the Mock upstream');
    const publicResult = await gateway.call(person, undefined, {method: 'GET', path: managementPath + '?consumer=probe',
      host, token: credential, binding: '', body: ''});
    assert(publicResult.status >= 400 && publicResult.status < 500, 'Private management became available on the public model listener');
    findings.completed = true;
    record('Private fixture management remains unavailable through the client model port');
  } catch (error) {
    primaryFailure = true;
    throw error;
  } finally {
    const cleanupErrors = [];
    for (const object of created.toReversed()) {
      try {
        await kubectl(['-n', 'higress-system', 'delete', object.kind, object.metadata.name, '--ignore-not-found']);
      } catch (error) {
        cleanupErrors.push(error);
      }
    }
    findings.cleanupSucceeded = cleanupErrors.length === 0;
    if (cleanupErrors.length && !primaryFailure) throw new AggregateError(cleanupErrors, 'Private fixture cleanup failed');
  }
}

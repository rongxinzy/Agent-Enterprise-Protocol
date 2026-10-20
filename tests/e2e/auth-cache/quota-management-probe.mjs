// Observe native management integration without weakening model authorization.
// Runs only against the disposable managed acceptance cluster and Mock provider.
import assert from 'node:assert/strict';
import {randomBytes} from 'node:crypto';
import {AepClient} from '../../../packages/aep-sdk-node/dist/index.js';

export async function quotaManagementProbe(ctx) {
  const {adminStore, gateway, person, kubectl, apply, forward, until, report, record} = ctx;
  assert(gateway?.nativeRedis, 'Quota management requires managed native Redis');
  let admin = ctx.admin;
  const credential = randomBytes(32).toString('hex');
  const {publication, adminPort, redis} = gateway.nativeRedis;
  const patchControl = ['-n', 'aep-system', 'patch', 'deployment', 'aep-business-control', '--type=strategic', '-p'];
  const findings = {completed: false, managementAvailable: false, listenerIsolationVerified: false, clientRequests: []};
  report.cumulativeQuotaProbe = findings;
  let configured = false;
  async function reconnect() {
    await kubectl(['-n', 'aep-system', 'rollout', 'status', 'deployment/aep-business-control', '--timeout=180s']);
    const baseUrl = 'http://127.0.0.1:' + await forward('deployment/aep-business-control', 8080);
    admin = new AepClient({baseUrl, tokenStore: adminStore});
    person.client = new AepClient({baseUrl, tokenStore: person.store});
  }
  const patchNative = reference => kubectl(['-n', 'aep-system', 'patch', 'deploymentconfig', 'deployment', '--type=merge', '-p',
    JSON.stringify({spec: {nativeGateway: {quotaAdminCredentialRef: reference}}})]);
  try {
    await apply([{apiVersion: 'v1', kind: 'Secret', metadata: {name: 'aep-native-gateway', namespace: 'higress-system'},
      stringData: {admin: credential}}]);
    await patchNative({name: 'aep-native-gateway', key: 'admin'});
    configured = true;
    await publication(admin);
    const quota = await until(async () => {
      const plugins = JSON.parse(await kubectl(['-n', 'higress-system', 'get', 'wasmplugins', '-o', 'json'])).items;
      const plugin = plugins.find(p => p.spec.matchRules?.some(r => r.config?.admin_consumer));
      assert(plugin, 'Operator quota plugin is missing');
      const name = plugin.metadata.name;
      const ingress = JSON.parse(await kubectl(['-n', 'higress-system', 'get', 'ingress', name, '-o', 'json']));
      const path = ingress.spec.rules[0].http.paths[0].path;
      const dump = await (await fetch('http://127.0.0.1:' + adminPort + '/config_dump', {signal: AbortSignal.timeout(5000)})).json();
      const extensions = dump.configs.flatMap(c => c.ecds_filters ?? []).map(e => e.ecds_filter?.name ?? '');
      assert(extensions.some(n => n.endsWith('.' + name)), 'Native quota extension is not loaded');
      assert(extensions.some(n => n.includes('.aep-quota-auth-')), 'Native quota key-auth guard is not loaded');
      const listeners = dump.configs.flatMap(c => c.dynamic_listeners ?? []).map(l => l.active_state?.listener).filter(Boolean);
      const clientListener = listeners.find(l => l.address?.socket_address?.port_value === 80);
      assert(clientListener, 'Client listener is not loaded');
      const filters = clientListener.filter_chains.flatMap(c => c.filters ?? [])
        .flatMap(f => f.typed_config?.http_filters ?? []).map(f => f.name);
      assert(filters.includes('envoy.filters.http.ext_authz'), 'Model authorization must remain enabled');
      const virtualHosts = dump.configs.flatMap(c => c.dynamic_route_configs ?? []).flatMap(c => c.route_config?.virtual_hosts ?? []);
      const hosts = virtualHosts.filter(v => v.routes?.some(r => JSON.stringify(r.match).includes(path)));
      assert(hosts.length, 'Native quota management route is not loaded');
      findings.managementRouteDomains = hosts.map(v => v.domains);
      return {name, path};
    });
    // Quota keys are separate from rate-limit keys. No key values/credentials
    // enter the report; unauthorized mutation must not create quota state.
    const quotaKeys = () => redis('--scan', '--pattern', 'aep_quota:*');
    const keysBefore = await quotaKeys();
    const upstreamBefore = await gateway.stats();
    const authorizationStats = async () => (await (await fetch('http://127.0.0.1:' + adminPort + '/stats?filter=ext_authz',
      {signal: AbortSignal.timeout(5000)})).text()).trim();
    findings.authorizationStatsBefore = await authorizationStats();
    for (const [name, options] of [
      ['no credential', {token: '', binding: ''}],
      ['ordinary model credential', {}],
      ['forged admin consumer', {headers: {'x-mse-consumer': quota.name, 'x-consumer-id': quota.name}}],
      ['management key on client listener', {token: credential, binding: ''}],
    ]) {
      for (const [operation, method, suffix, body] of [
        ['read', 'GET', '?consumer=probe', ''],
        ['refresh', 'POST', '/refresh', 'consumer=probe&quota=7'],
      ]) {
        const response = await gateway.call(person, undefined, {method, path: quota.path + suffix, body,
          host: 'higress-gateway.higress-system.svc', ...options,
          headers: {'content-type': 'application/x-www-form-urlencoded', ...options.headers}});
        findings.clientRequests.push({name, operation, status: response.status});
        assert(response.status >= 400 && response.status < 500, name + ' reached native quota management');
      }
    }
    findings.authorizationStatsAfter = await authorizationStats();
    assert.equal(await quotaKeys(), keysBefore, 'Rejected management traffic created native quota state');
    assert.equal(await gateway.stats(), upstreamBefore, 'Management requests reached the model provider');
    findings.clientManagementRejected = true;
    record('Client listener denies absent, model, forged consumer and management credentials on native quota read/refresh paths');

    await apply([{apiVersion: 'v1', kind: 'Secret', metadata: {name: 'access-quota-control-probe', namespace: 'aep-system'},
      stringData: {token: credential}}]);
    await kubectl([...patchControl, JSON.stringify({spec: {template: {spec: {containers: [{name: 'service', env: [
      {name: 'AEP_GATEWAY_QUOTA_URL', value: 'http://higress-gateway.higress-system.svc' + quota.path},
      {name: 'AEP_GATEWAY_QUOTA_TOKEN', valueFrom: {secretKeyRef: {name: 'access-quota-control-probe', key: 'token'}}},
    ]}]}}}})]);
    await reconnect();
    try {
      await admin.getGatewayQuota(person.user.id);
      findings.apiStatus = 200;
      findings.managementAvailable = true;
    } catch (error) {
      assert.equal(error.status, 503, 'Unexpected cumulative quota API error');
      findings.apiStatus = error.status;
      findings.apiProblemCode = error.code;
    }
    // Rejection alone is not working isolation: the legitimate management
    // path must also work, without disabling ext_authz on the client listener.
    findings.listenerIsolationVerified = findings.managementAvailable && findings.clientManagementRejected;
    findings.blocker = !findings.listenerIsolationVerified;
    findings.completed = true;
    report.persistentQuotaAPIIntegrated = findings.listenerIsolationVerified;
    console.log('OBSERVATION: cumulative quota management ' + (findings.blocker ? 'is an adoption blocker' : 'is integrated'));
  } finally {
    if (configured) {
      await patchNative(null);
      await publication(admin);
      await kubectl([...patchControl, JSON.stringify({spec: {template: {spec: {containers: [{name: 'service', env: [
        {name: 'AEP_GATEWAY_QUOTA_URL', $patch: 'delete'}, {name: 'AEP_GATEWAY_QUOTA_TOKEN', $patch: 'delete'},
      ]}]}}}})]);
      await reconnect();
    }
    await kubectl(['-n', 'higress-system', 'delete', 'secret', 'aep-native-gateway', '--ignore-not-found']);
    await kubectl(['-n', 'aep-system', 'delete', 'secret', 'access-quota-control-probe', '--ignore-not-found']);
    await until(async () => assert.equal((await gateway.call(person)).status, 200));
    record('Cumulative quota probe removes temporary configuration and restores normal inference');
  }
  return admin;
}

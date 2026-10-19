// Native Envoy routing/ext_authz fixture, not a product identity protocol.
import assert from 'node:assert/strict';
import http from 'node:http';
import {namespace, policy} from './resources.mjs';

export const gatewayImage = 'envoyproxy/envoy:v1.36.4@sha256:08acc53db6f4ac46e77e36b203c5254b4a2549869314bb54cf97aab81baa4566';

function gatewayResources() {
  const metadata = name => ({name, namespace});
  const objects = [];
  for (const replica of ['a', 'b']) {
    const name = 'gateway-' + replica;
    const routes = ['alice', 'bob'].map(subject => ({
      match: {path: '/v1/chat/completions', headers: [{name: 'x-probe-principal', string_match: {exact: subject}}]},
      route: {cluster: 'upstream', prefix_rewrite: '/echo'},
      typed_per_filter_config: {'envoy.filters.http.ext_authz': {
        '@type': 'type.googleapis.com/envoy.extensions.filters.http.ext_authz.v3.ExtAuthzPerRoute',
        check_settings: {context_extensions: {host: 'gateway-' + subject + '.probe.invalid'}},
      }},
    }));
    routes.push({match: {prefix: '/'}, direct_response: {status: 403}});
    const config = {static_resources: {
      listeners: [{name: 'shared', address: {socket_address: {address: '0.0.0.0', port_value: 8080}}, filter_chains: [{filters: [{
        name: 'envoy.filters.network.http_connection_manager', typed_config: {
          '@type': 'type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager',
          stat_prefix: 'probe', route_config: {name: 'shared', virtual_hosts: [{name: 'shared', domains: ['gateway.probe.invalid'], routes}]},
          http_filters: [{name: 'envoy.filters.http.ext_authz', typed_config: {
            '@type': 'type.googleapis.com/envoy.extensions.filters.http.ext_authz.v3.ExtAuthz',
            transport_api_version: 'V3', failure_mode_allow: false,
            with_request_body: {max_request_bytes: 8192, allow_partial_message: false},
            grpc_service: {envoy_grpc: {cluster_name: 'authorization'}, timeout: '2s'},
          }}, {name: 'envoy.filters.http.router', typed_config: {'@type': 'type.googleapis.com/envoy.extensions.filters.http.router.v3.Router'}}],
        },
      }]}]}],
      clusters: [
        {name: 'authorization', port: 50051, host: 'authorino-' + replica, http2_protocol_options: {}},
        {name: 'upstream', port: 8080, host: 'introspection'},
      ].map(({name: cluster, host, port, ...extra}) => ({name: cluster, type: 'STRICT_DNS', connect_timeout: '1s', ...extra,
        load_assignment: {cluster_name: cluster, endpoints: [{lb_endpoints: [{endpoint: {address: {socket_address: {address: host, port_value: port}}}}]}]}})),
    }};
    objects.push(
      {apiVersion: 'v1', kind: 'Service', metadata: metadata('authorino-' + replica),
        spec: {selector: {app: 'authorino-' + replica}, ports: [{port: 50051, targetPort: 50051}]}},
      {apiVersion: 'v1', kind: 'ConfigMap', metadata: metadata(name), data: {'envoy.json': JSON.stringify(config)}},
      {apiVersion: 'apps/v1', kind: 'Deployment', metadata: metadata(name), spec: {replicas: 1, selector: {matchLabels: {app: name}},
        template: {metadata: {labels: {app: name}}, spec: {automountServiceAccountToken: false, containers: [{name,
          image: gatewayImage, args: ['-c', '/config/envoy.json', '--concurrency', '1', '--log-level', 'error'],
          securityContext: {runAsNonRoot: true, runAsUser: 101, allowPrivilegeEscalation: false, capabilities: {drop: ['ALL']}},
          resources: {requests: {cpu: '10m', memory: '32Mi'}, limits: {memory: '128Mi'}},
          readinessProbe: {tcpSocket: {port: 8080}, periodSeconds: 2},
          volumeMounts: [{name: 'config', mountPath: '/config', readOnly: true}],
        }], volumes: [{name: 'config', configMap: {name}}]}}}},
    );
  }
  return objects;
}

function grant(subject, model = 'model-a', revision = '1', validUntil = Date.now() / 1000 + 300) {
  const object = policy('gateway-' + subject, {subject, revision});
  object.spec.hosts = ['gateway-' + subject + '.probe.invalid'];
  // Cache the complete allowed range, independent of prompt or selected model.
  object.spec.authorization.access.opa.rego = `allow {
input.auth.identity.deployment_id == "probe"
input.auth.identity.sub == ${JSON.stringify(subject)}
}
models = ${JSON.stringify([model])}
stamp = time.now_ns()`;
  object.spec.authorization.access.cache.key = {selector: 'context.request.http.headers.authorization'};
  // These cheap request range/validity guards do not repeat role expansion.
  object.spec.authorization.bounds = {priority: 1, opa: {rego: `allow {
input.auth.identity.exp > time.now_ns() / 1000000000
time.now_ns() / 1000000000 < ${validUntil}
body := json.unmarshal(input.context.request.http.body)
input.auth.authorization.access.models[_] == body.model
input.auth.identity.model_scopes[_] == body.model
}`}};
  return object;
}

function request(port, {subject = 'alice', token = 'probe-' + subject + '-session', model = 'model-a', prompt = 'first', headers = {}} = {}) {
  return new Promise((resolve, reject) => {
    const req = http.request({host: '127.0.0.1', port, path: '/v1/chat/completions', method: 'POST', timeout: 5000,
      headers: {host: 'gateway.probe.invalid', authorization: 'Bearer ' + token, 'content-type': 'application/json', 'x-probe-principal': subject, ...headers}}, res => {
      let body = '';
      res.on('data', chunk => body += chunk);
      res.on('end', () => {
        try {resolve({status: res.statusCode, ...(res.statusCode === 200 ? JSON.parse(JSON.parse(body).witness) : {})});}
        catch {reject(new Error('Invalid shared gateway witness'));}
      });
    });
    req.on('error', reject);
    req.on('timeout', () => req.destroy(new Error('Shared gateway request timed out')));
    req.end(JSON.stringify({model, messages: [{role: 'user', content: prompt}]}));
  });
}

export async function sharedGateway({apply, publish, kubectl, forward, stats, record, until}) {
  await apply(gatewayResources());
  for (const name of ['gateway-a', 'gateway-b']) await kubectl(['-n', namespace, 'rollout', 'status', 'deployment/' + name, '--timeout=180s']);
  const ports = await Promise.all(['gateway-a', 'gateway-b'].map(name => forward('deployment/' + name, 8080)));
  await publish([grant('alice'), grant('bob')]);
  const bobWarm = [], aliceWarm = [];
  for (const port of ports) {
    for (const [subject, values] of [['alice', aliceWarm], ['bob', bobWarm]]) {
      const first = await until(async () => {const result = await request(port, {subject}); assert.equal(result.status, 200); assert(result.identity && result.decision); return result;});
      values.push(first);
      assert.deepEqual(await request(port, {subject, prompt: 'different prompt'}), first);
    }
  }
  record('Shared host with native ext_authz reuses identity and complete allowed range across different prompts on both replicas');
  for (const port of ports) {
    assert.equal((await request(port, {subject: 'bob', token: 'probe-alice-session'})).status, 403);
    assert.equal((await request(port, {token: 'unknown'})).status, 401);
    assert.equal((await request(port, {model: 'model-b', headers: {'x-aep-model-id': 'model-a'}})).status, 403);
    // Authorino has no config for the shared host when no principal route matches.
    assert.equal((await request(port, {subject: 'unknown'})).status, 404);
    assert.equal((await request(port, {subject: 'bob', token: 'probe-alice-session', headers: {
      'x-aep-user-id': 'bob', 'x-mse-consumer': 'bob', 'x-ext-auth-host': 'gateway-bob.probe.invalid',
    }})).status, 403);
  }
  record('Untrusted principal hints, identity headers, unknown credentials and out-of-range body models cannot grant access');
  const before = await stats();
  await publish([grant('alice', 'model-b', '2')]);
  for (const [i, port] of ports.entries()) {
    const changed = await until(async () => {const result = await request(port, {model: 'model-b'}); assert.equal(result.status, 200); assert.equal(result.revision, '2'); return result;});
    assert.notEqual(changed.identity, aliceWarm[i].identity);
    assert.notEqual(changed.decision, aliceWarm[i].decision);
    assert.equal((await request(port)).status, 403);
    assert.deepEqual(await request(port, {subject: 'bob'}), bobWarm[i]);
  }
  const after = await stats();
  assert.equal(after.calls['session-a'] - before.calls['session-a'], 2);
  assert.equal(after.calls['session-b'], before.calls['session-b']);
  record('Shared gateway publication reauthenticates only Alice and preserves Bob on both serving paths');

  // A fixed publication validity cap bounds stale results without a per-request DB call.
  const expires = Date.now() / 1000 + 5;
  await publish([grant('alice', 'model-b', '3', expires)]);
  for (const port of ports) await until(async () => {
    const result = await request(port, {model: 'model-b'});
    assert.equal(result.status, 200);
    assert.equal(result.revision, '3');
  });
  const warmCalls = (await stats()).calls['session-a'];
  await until(async () => assert(Date.now() / 1000 > expires + 0.2));
  for (const port of ports) assert.equal((await request(port, {model: 'model-b'})).status, 403);
  assert.equal((await stats()).calls['session-a'], warmCalls);
  record('Publication validity cap rejects cached grants when renewal stops without introspection');
}

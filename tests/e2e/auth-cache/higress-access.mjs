// A shared real Higress entry using only native Envoy gRPC ext_authz.
// Binding is an untrusted experiment hint, NOT a new public AEP contract.
import assert from 'node:assert/strict';
import http from 'node:http';

const namespace = 'aep-system';
const gatewayNamespace = 'higress-system';
const forbidden = ['authorization', 'x-aep-access-binding', 'x-aep-access-observation', 'x-aep-user-id',
  'x-aep-team-ids', 'x-aep-role-ids', 'x-aep-session-id', 'x-aep-agent-id', 'x-aep-deployment-id',
  'x-aep-model-id', 'x-mse-consumer', 'x-consumer-id', 'x-ext-auth-host'];

function fixture(bindings) {
  const metadata = name => ({name, namespace});
  // Return only presence booleans: never echo bearer credentials or body text.
  const code = `import http from 'node:http';
let calls = 0;
const forbidden = ${JSON.stringify(forbidden)};
http.createServer((req, res) => {
  if (req.url === '/stats') {res.end(JSON.stringify({calls})); return;}
  calls++;
  req.resume();
  req.on('end', () => {res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify({path: req.url, host: req.headers.host,
      leaked: Object.keys(req.headers).filter(name => forbidden.includes(name) || name.startsWith('x-aep-'))}));});
}).listen(8080, '0.0.0.0');`;
  return [
    {apiVersion: 'v1', kind: 'Service', metadata: metadata('access-authorization'), spec: {
      selector: {'aep-acceptance-authorino': 'true'}, ports: [{name: 'grpc', port: 50051}]}},
    {apiVersion: 'v1', kind: 'ConfigMap', metadata: metadata('access-upstream'), data: {'server.mjs': code}},
    {apiVersion: 'apps/v1', kind: 'Deployment', metadata: metadata('access-upstream'), spec: {replicas: 1,
      selector: {matchLabels: {app: 'access-upstream'}}, template: {metadata: {labels: {app: 'access-upstream'}}, spec: {
        automountServiceAccountToken: false, securityContext: {runAsNonRoot: true, runAsUser: 1000, seccompProfile: {type: 'RuntimeDefault'}},
        containers: [{name: 'echo', image: 'node:24-alpine', command: ['node', '/code/server.mjs'],
          securityContext: {allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: ['ALL']}},
          resources: {requests: {cpu: '10m', memory: '32Mi'}, limits: {memory: '128Mi'}},
          readinessProbe: {tcpSocket: {port: 8080}, periodSeconds: 2},
          volumeMounts: [{name: 'code', mountPath: '/code', readOnly: true}]}],
        volumes: [{name: 'code', configMap: {name: 'access-upstream'}}],
      }}}},
    {apiVersion: 'v1', kind: 'Service', metadata: metadata('access-upstream'), spec: {
      selector: {app: 'access-upstream'}, ports: [{name: 'http', port: 8080}]}},
    {apiVersion: 'networking.k8s.io/v1', kind: 'Ingress', metadata: metadata('access-shared'), spec: {ingressClassName: 'higress',
      rules: [{host: 'shared.access.test', http: {paths: [{path: '/v1', pathType: 'Prefix',
        backend: {service: {name: 'access-upstream', port: {number: 8080}}}}]}}]}},
    {apiVersion: 'networking.istio.io/v1alpha3', kind: 'EnvoyFilter', metadata: {name: 'access-shared', namespace: gatewayNamespace},
      spec: {workloadSelector: {labels: {app: 'higress-gateway'}}, configPatches: [
        {applyTo: 'CLUSTER', match: {context: 'GATEWAY'}, patch: {operation: 'ADD', value: {
          name: 'aep-access-authorization', type: 'STRICT_DNS', connect_timeout: '2s', http2_protocol_options: {},
          load_assignment: {cluster_name: 'aep-access-authorization', endpoints: [{lb_endpoints: [{endpoint: {
            address: {socket_address: {address: 'access-authorization.aep-system.svc.cluster.local', port_value: 50051}},
          }}]}]},
        }}},
        {applyTo: 'HTTP_FILTER', match: {context: 'GATEWAY', listener: {filterChain: {filter: {
          name: 'envoy.filters.network.http_connection_manager', subFilter: {name: 'envoy.filters.http.router'},
        }}}}, patch: {operation: 'INSERT_BEFORE', value: {name: 'envoy.filters.http.ext_authz', typed_config: {
          '@type': 'type.googleapis.com/envoy.extensions.filters.http.ext_authz.v3.ExtAuthz',
          failure_mode_allow: false, transport_api_version: 'V3',
          with_request_body: {max_request_bytes: 8192, allow_partial_message: false},
          grpc_service: {envoy_grpc: {cluster_name: 'aep-access-authorization'}, timeout: '2s'},
        }}}},
        {applyTo: 'CLUSTER', match: {context: 'GATEWAY'}, patch: {operation: 'ADD', value: {
          name: 'aep-access-upstream', type: 'STRICT_DNS', connect_timeout: '2s',
          load_assignment: {cluster_name: 'aep-access-upstream', endpoints: [{lb_endpoints: [{endpoint: {
            address: {socket_address: {address: 'access-upstream.aep-system.svc.cluster.local', port_value: 8080}},
          }}]}]},
        }}},
        // Explicit per-session fixture routes. Production requires an owned,
        // capacity-tested projection, not this acceptance-side route writer.
        ...[...bindings].map(binding => ({applyTo: 'HTTP_ROUTE', match: {context: 'GATEWAY'}, patch: {operation: 'INSERT_BEFORE', value: {
          name: 'access-' + binding,
          match: {prefix: '/v1/', headers: [{name: 'x-aep-access-binding', string_match: {exact: binding}}]},
          route: {cluster: 'aep-access-upstream'},
          typed_per_filter_config: {'envoy.filters.http.ext_authz': {
            '@type': 'type.googleapis.com/envoy.extensions.filters.http.ext_authz.v3.ExtAuthzPerRoute',
            check_settings: {context_extensions: {host: binding + '.access.aep.invalid'}},
          }},
        }}})),
        // Route header removal happens before forwarding to the model upstream.
        // Authorization never trusts these identity/consumer headers beforehand.
        {applyTo: 'VIRTUAL_HOST', match: {context: 'GATEWAY'}, patch: {operation: 'MERGE', value: {request_headers_to_remove: forbidden}}},
      ]}},
  ];
}

function request(port, person, model, {binding = person.binding, token = person.tokens.modelAccessToken, headers = {}, body} = {}) {
  return new Promise((resolve, reject) => {
    const req = http.request({host: '127.0.0.1', port, path: '/v1/chat/completions', method: 'POST', timeout: 6000,
      headers: {host: 'shared.access.test', authorization: 'Bearer ' + token, 'content-type': 'application/json',
        'x-aep-access-binding': binding, ...headers}}, res => {
      let response = '';
      res.on('data', chunk => response += chunk);
      res.on('end', () => {
        try {resolve({status: res.statusCode, observationLeaked: Boolean(res.headers['x-aep-access-observation']),
          ...(res.statusCode === 200 ? {upstream: JSON.parse(response)} : {})});}
        catch {reject(new Error('Invalid Higress upstream witness'));}
      });
    });
    req.on('error', reject); req.on('timeout', () => req.destroy(new Error('Higress request timeout')));
    req.end(body ?? JSON.stringify({model, messages: [{role: 'user', content: 'disposable gateway probe'}]}));
  });
}

export async function higressAccess({apply, kubectl, forward, until, report, record}, alice, bob, model) {
  const bindings = new Set([alice.binding, bob.binding]);
  await apply(fixture(bindings));
  await kubectl(['-n', namespace, 'rollout', 'status', 'deployment/access-upstream', '--timeout=120s']);
  const port = await forward('deployment/higress-gateway', 80, gatewayNamespace);
  const upstreamPort = await forward('deployment/access-upstream', 8080);
  const stats = async () => (await (await fetch('http://127.0.0.1:' + upstreamPort + '/stats', {signal: AbortSignal.timeout(5000)})).json()).calls;
  async function check(person, status, id = model.id) {
    if (!bindings.has(person.binding)) {
      assert.match(person.binding, /^[a-f0-9]{64}$/);
      bindings.add(person.binding);
      await apply(fixture(bindings).filter(object => object.kind === 'EnvoyFilter'));
    }
    const result = await request(port, person, id);
    assert.equal(result.status, status, 'Higress status mismatch');
    if (status === 200) {
      assert.deepEqual(result.upstream.leaked, [], 'Untrusted identity or bearer reached upstream');
      assert.equal(result.upstream.host, 'shared.access.test', 'Internal binding host must not replace actual upstream host');
      assert.equal(result.upstream.path, '/v1/chat/completions', 'Authorization path must not replace inference path');
    }
    assert.equal(result.observationLeaked, false, 'Internal observation leaked to client');
  }
  await until(async () => check(alice, 200)); await until(async () => check(bob, 200));
  report.scope = 'real business API -> DB/outbox/relay -> CR/Operator -> native Authorino -> real shared Higress -> credential-free echo upstream';
  report.higress = '2.2.4'; report.bindingHintIsPublicContract = false; report.quotaIdentityVerified = false;
  report.productionBindingRouteProjectionImplemented = false;
  report.reservedHeaderPrefixSanitizationImplemented = false;
  record('Real Higress shared public Host uses native gRPC per-route context to select the binding; JWT still authenticates it and inference host/path are preserved');
  const before = await stats();
  const spoofed = Object.fromEntries(forbidden.filter(name => !['authorization', 'x-aep-access-binding'].includes(name)).map(name => [name, 'forged']));
  for (const options of [{binding: bob.binding}, {binding: ''}, {binding: 'unknown'}, {binding: 'alice.example.invalid'},
    {token: 'unknown'}, {token: ''}, {binding: bob.binding, headers: spoofed},
    {body: JSON.stringify({model: 'unassigned-model'}), headers: {'x-aep-model-id': model.id}},
    ...['{', 'null', '[]', '{}', '{"model":42}', '{"model":["' + model.id + '"]}'].map(body => ({body})),
    {body: JSON.stringify({model: model.id, padding: 'x'.repeat(9000)})}]) {
    const result = await request(port, alice, model.id, options);
    assert(result.status >= 400 && result.status < 500, 'Malformed/escalated request must deny');
    assert.equal(result.observationLeaked, false);
  }
  assert.equal(await stats(), before, 'Denied requests must not contact upstream');
  await check({...alice}, 200);
  const sanitized = await request(port, alice, model.id, {headers: spoofed});
  assert.equal(sanitized.status, 200); assert.deepEqual(sanitized.upstream.leaked, []);
  record('Cross-user/missing/unknown binding, invalid bearer, forged identity, actual model escalation, malformed/oversized body all deny without an upstream call; allowed requests strip explicitly listed sensitive headers');
  const reserved = ['x-aep-limit-' + 'a'.repeat(64), 'x-aep-unlisted-probe'];
  const residual = await request(port, alice, model.id, {headers: Object.fromEntries(reserved.map(name => [name, '1']))});
  assert.equal(residual.status, 200); assert.deepEqual(residual.upstream.leaked.sort(), reserved.sort());
  record('LIMITATION: exact route removal does not sanitize the whole reserved header namespace; trusted quota integration must not adopt this fixture', {knownLimitation: true});
  return {check, async outage(person) {
    const before = await stats();
    for (const name of ['authorino-a', 'authorino-b']) await kubectl(['-n', namespace, 'scale', 'deployment/' + name, '--replicas=0']);
    try {
      await until(async () => {
        const pods = JSON.parse(await kubectl(['-n', namespace, 'get', 'pods', '-l', 'aep-acceptance-authorino=true', '-o', 'json'])).items;
        assert.equal(pods.length, 0);
      });
      const result = await request(port, person, model.id);
      assert(result.status >= 400 && result.status < 600, 'Unavailable authorization must fail closed');
      assert.equal(await stats(), before, 'Authorization outage must not forward upstream');
      record('Real Higress fails closed when every Authorino process is stopped, with no upstream request');
    } finally {
      for (const name of ['authorino-a', 'authorino-b']) {
        await kubectl(['-n', namespace, 'scale', 'deployment/' + name, '--replicas=1']);
        await kubectl(['-n', namespace, 'rollout', 'status', 'deployment/' + name, '--timeout=150s']);
      }
    }
    await until(async () => check(person, 200));
    record('Shared Higress entry recovers after native authorization processes restart');
  }};
}

export const namespace = 'auth-cache-probe';
export const sourceRevision = '8232753b58bec6994f5d914d239cc410fcf5173f';
export const crdHash = 'ccaf7e85c8e678d42dc87050f79e1cd2bd92fefe1a75511577c3f3513fb0b15c';
export const image = 'quay.io/kuadrant/authorino:v0.26.4@sha256:67273e9a2619ba832e710cf3177aee2615c0c41ec49198ec17a9882aeac5d010';

export function resources(mockSource) {
  const metadata = name => ({name, namespace});
  const deployment = (name, container, serviceAccountName) => ({apiVersion: 'apps/v1', kind: 'Deployment', metadata: metadata(name),
    spec: {replicas: 1, selector: {matchLabels: {app: name}}, template: {metadata: {labels: {app: name}}, spec: {
      ...(serviceAccountName ? {serviceAccountName} : {automountServiceAccountToken: false}),
      containers: [{name, securityContext: {allowPrivilegeEscalation: false, capabilities: {drop: ['ALL']}},
        resources: {requests: {cpu: '10m', memory: '64Mi'}, limits: {memory: '256Mi'}}, ...container}],
      ...(name === 'introspection' ? {volumes: [{name: 'code', configMap: {name: 'introspection'}}]} : {}),
    }}}});
  return [
    {apiVersion: 'v1', kind: 'Namespace', metadata: {name: namespace}},
    {apiVersion: 'v1', kind: 'ServiceAccount', metadata: metadata('authorino')},
    {apiVersion: 'rbac.authorization.k8s.io/v1', kind: 'Role', metadata: metadata('authorino'), rules: [
      {apiGroups: [''], resources: ['secrets'], verbs: ['get', 'list', 'watch']},
      {apiGroups: ['authorino.kuadrant.io'], resources: ['authconfigs'], verbs: ['get', 'list', 'watch']},
      {apiGroups: ['authorino.kuadrant.io'], resources: ['authconfigs/status'], verbs: ['get', 'patch', 'update']},
    ]},
    {apiVersion: 'rbac.authorization.k8s.io/v1', kind: 'RoleBinding', metadata: metadata('authorino'),
      subjects: [{kind: 'ServiceAccount', name: 'authorino', namespace}],
      roleRef: {apiGroup: 'rbac.authorization.k8s.io', kind: 'Role', name: 'authorino'}},
    {apiVersion: 'v1', kind: 'Secret', metadata: metadata('introspection-client'), type: 'Opaque',
      stringData: {clientID: 'disposable-probe', clientSecret: 'disposable-probe-only'}},
    {apiVersion: 'v1', kind: 'ConfigMap', metadata: metadata('introspection'), data: {'server.mjs': mockSource}},
    deployment('introspection', {image: 'node:24-alpine', command: ['node', '/code/server.mjs'],
      ports: [{containerPort: 8080}], volumeMounts: [{name: 'code', mountPath: '/code', readOnly: true}]}),
    {apiVersion: 'v1', kind: 'Service', metadata: metadata('introspection'),
      spec: {selector: {app: 'introspection'}, ports: [{port: 8080, targetPort: 8080}]}},
    ...['authorino-a', 'authorino-b'].map(name => deployment(name, {image,
      args: ['--watch-namespace=' + namespace, '--oidc-http-port=0', '--ext-auth-grpc-port=0', '--evaluator-cache-size=1'],
      readinessProbe: {httpGet: {path: '/readyz', port: 8081}, initialDelaySeconds: 2, periodSeconds: 2},
      ports: [{containerPort: 5001}], securityContext: {runAsNonRoot: true, runAsUser: 1000,
        allowPrivilegeEscalation: false, capabilities: {drop: ['ALL']}},
    }, 'authorino')),
  ];
}

export function policy(name, {subject, model = 'model-a', revision = '1', sessions, freshness = false} = {}) {
  const shared = subject === undefined;
  const rules = [
    'input.auth.identity.deployment_id == "probe"',
    'model := input.context.request.http.headers["x-aep-model-id"]',
    shared ? `${JSON.stringify({alice: model, bob: 'model-a'})}[input.auth.identity.sub] == model` : `model == ${JSON.stringify(model)}`,
    'input.auth.identity.model_scopes[_] == model',
    ...(shared ? [] : [`input.auth.identity.sub == ${JSON.stringify(subject)}`]),
    ...(sessions ? [`${JSON.stringify(sessions)}[_] == input.auth.identity.sid`] : []),
  ];
  const authorization = {access: {
    opa: {rego: `allow {\n${rules.join('\n')}\n}\nstamp = time.now_ns()`, allValues: true},
    cache: {key: {expression: 'request.headers.authorization + "|" + request.headers["x-aep-model-id"]'}, ttl: 300},
  }};
  if (freshness) authorization.freshness = {opa: {rego: 'allow { input.auth.identity.exp > time.now_ns() / 1000000000 }'}};
  return {apiVersion: 'authorino.kuadrant.io/v1beta3', kind: 'AuthConfig', metadata: {name, namespace}, spec: {
    hosts: [(shared ? 'shared' : subject) + '.probe.invalid'],
    authentication: {session: {
      oauth2Introspection: {endpoint: 'http://introspection:8080/introspect', credentialsRef: {name: 'introspection-client'}},
      cache: {key: {selector: 'context.request.http.headers.authorization'}, ttl: 300},
    }}, authorization, response: {success: {headers: {
      'x-probe-witness': {json: {properties: {
        identity: {selector: 'auth.identity.serial'},
        decision: {selector: 'auth.authorization.access.stamp'},
        revision: {value: revision},
      }}},
    }}},
  }};
}

// Disposable real control-service/PostgreSQL/object-store stack for acceptance.
import assert from 'node:assert/strict';
import {randomBytes} from 'node:crypto';
import {readFile, rm} from 'node:fs/promises';
import path from 'node:path';

export async function businessStack({command, kubectl, apply, forward, until, work, cluster}) {
  const namespace = 'aep-system';
  const issuer = 'http://aep-business-control.aep-system.svc:8080';
  const password = randomBytes(24).toString('hex');
  const dbPassword = randomBytes(24).toString('hex');
  const objectPassword = randomBytes(24).toString('hex');
  const refs = path.join(work, 'control-image.txt');
  console.log('Building real control-service with ko');
  await command(process.env.KO ?? 'go', [...(process.env.KO ? [] : ['run', 'github.com/google/ko@v0.18.0']),
    'build', '--platform', 'linux/amd64', '--image-refs', refs, './services/control-service/cmd/server'],
  {commandEnv: {...process.env, KO_DOCKER_REPO: 'ko.local'}, timeout: 600_000});
  const controlImage = (await readFile(refs, 'utf8')).trim();
  for (const [index, ref] of ['postgres:17.6-alpine', 'rustfs/rustfs:1.0.0', controlImage].entries()) {
    const archive = path.join(work, 'business-image-' + index + '.tar');
    await command('docker', ['image', 'save', '--platform=linux/amd64', '-o', archive, ref]);
    await command(process.env.KIND ?? 'kind', ['load', 'image-archive', '--name', cluster, archive]);
    await rm(archive);
  }
  const metadata = name => ({name, namespace});
  const service = name => ({apiVersion: 'v1', kind: 'Service', metadata: metadata(name),
    spec: {selector: {app: name}, ports: [{port: name === 'aep-business-db' ? 5432 : name === 'aep-business-object' ? 9000 : 8080}]}});
  function deployment(name, container, user, volumes) {
    return {apiVersion: 'apps/v1', kind: 'Deployment', metadata: metadata(name), spec: {replicas: 1,
      selector: {matchLabels: {app: name}}, template: {metadata: {labels: {app: name}}, spec: {
        automountServiceAccountToken: false,
        securityContext: {runAsNonRoot: true, runAsUser: user, runAsGroup: user, fsGroup: user, seccompProfile: {type: 'RuntimeDefault'}},
        containers: [{name: 'service', securityContext: {allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: ['ALL']}},
          resources: {requests: {cpu: '20m', memory: '64Mi'}, limits: {memory: '512Mi'}}, ...container}], volumes,
      }}}};
  }
  const secretEnv = name => [{secretRef: {name}}];
  const db = deployment('aep-business-db', {image: 'postgres:17.6-alpine', envFrom: secretEnv('aep-business-db'),
    env: [{name: 'PGDATA', value: '/var/lib/postgresql/data/pgdata'}],
    readinessProbe: {exec: {command: ['pg_isready', '-U', 'aep', '-d', 'aep']}, periodSeconds: 2},
    volumeMounts: [{name: 'data', mountPath: '/var/lib/postgresql/data'}, {name: 'socket', mountPath: '/var/run/postgresql'}, {name: 'tmp', mountPath: '/tmp'}]},
  70, ['data', 'socket', 'tmp'].map(name => ({name, emptyDir: {}})));
  const object = deployment('aep-business-object', {image: 'rustfs/rustfs:1.0.0', envFrom: secretEnv('aep-business-object'),
    env: [{name: 'RUSTFS_VOLUMES', value: '/data'}, {name: 'RUSTFS_ADDRESS', value: '0.0.0.0:9000'},
      {name: 'RUSTFS_CONSOLE_ENABLE', value: 'false'}, {name: 'RUSTFS_OBS_LOG_DIRECTORY', value: '/tmp'}],
    volumeMounts: [{name: 'data', mountPath: '/data'}, {name: 'tmp', mountPath: '/tmp'}]},
  10001, ['data', 'tmp'].map(name => ({name, emptyDir: {}})));
  const config = {
    AEP_ENVIRONMENT: 'test', AEP_LOG_LEVEL: 'warn', AEP_ADDRESS: ':8080', AEP_ISSUER: issuer,
    AEP_DEPLOYMENT_ID: 'probe', AEP_BOOTSTRAP_ENTERPRISE_ID: 'probe', AEP_BOOTSTRAP_ADMIN_USERNAME: 'admin',
    AEP_MINIO_ENDPOINT: 'aep-business-object:9000', AEP_MINIO_BUCKET: 'access-acceptance',
    AEP_GATEWAY_PUBLICATIONS_ENABLED: 'true', AEP_GATEWAY_OPERATOR_MODE: 'true',
    AEP_GATEWAY_PUBLICATION_RELAY_ENABLED: 'true', AEP_GATEWAY_PUBLICATION_NAMESPACE: namespace,
    AEP_GATEWAY_ACCESS_RELAY_ENABLED: 'true', AEP_GATEWAY_ACCESS_FRESHNESS: '40s',
    AEP_RETENTION_CLEANUP_INTERVAL: '0',
  };
  const control = deployment('aep-business-control', {image: controlImage,
    envFrom: [{configMapRef: {name: 'aep-business-control'}}, ...secretEnv('aep-business-control')],
    readinessProbe: {httpGet: {path: '/readyz', port: 8080}, periodSeconds: 2},
    volumeMounts: [{name: 'api-access', mountPath: '/var/run/secrets/kubernetes.io/serviceaccount', readOnly: true}]},
  65532, [{name: 'api-access', projected: {defaultMode: 0o440, sources: [
    {serviceAccountToken: {path: 'token', expirationSeconds: 3600}},
    {configMap: {name: 'kube-root-ca.crt', items: [{key: 'ca.crt', path: 'ca.crt'}]}},
    {downwardAPI: {items: [{path: 'namespace', fieldRef: {fieldPath: 'metadata.namespace'}}]}},
  ]}}]);
  control.spec.template.spec.serviceAccountName = 'aep-business-control';
  await apply([
    {apiVersion: 'v1', kind: 'Secret', metadata: metadata('aep-business-db'), stringData: {POSTGRES_DB: 'aep', POSTGRES_USER: 'aep', POSTGRES_PASSWORD: dbPassword}},
    {apiVersion: 'v1', kind: 'Secret', metadata: metadata('aep-business-object'), stringData: {RUSTFS_ACCESS_KEY: 'access-acceptance', RUSTFS_SECRET_KEY: objectPassword}},
    {apiVersion: 'v1', kind: 'Secret', metadata: metadata('aep-business-control'), stringData: {
      AEP_BOOTSTRAP_ADMIN_PASSWORD: password, AEP_DATABASE_URL: 'postgres://aep:' + dbPassword + '@aep-business-db:5432/aep?sslmode=disable',
      AEP_MINIO_ACCESS_KEY: 'access-acceptance', AEP_MINIO_SECRET_KEY: objectPassword,
      AEP_SIGNING_KEY_BASE64: randomBytes(32).toString('base64'), AEP_CREDENTIAL_MASTER_KEY_BASE64: randomBytes(32).toString('base64'),
    }},
    {apiVersion: 'v1', kind: 'ConfigMap', metadata: metadata('aep-business-control'), data: config},
    {apiVersion: 'v1', kind: 'ServiceAccount', metadata: metadata('aep-business-control')},
    {apiVersion: 'rbac.authorization.k8s.io/v1', kind: 'Role', metadata: metadata('aep-business-control'), rules: [
      {apiGroups: ['gateway.aep.zhiyuan.io'], resources: ['deploymentconfigs'], verbs: ['get']},
      {apiGroups: ['gateway.aep.zhiyuan.io'], resources: ['modelaccessgrants'], verbs: ['get', 'create', 'update']},
    ]},
    {apiVersion: 'rbac.authorization.k8s.io/v1', kind: 'RoleBinding', metadata: metadata('aep-business-control'),
      subjects: [{kind: 'ServiceAccount', name: 'aep-business-control', namespace}],
      roleRef: {apiGroup: 'rbac.authorization.k8s.io', kind: 'Role', name: 'aep-business-control'}},
    {apiVersion: 'gateway.aep.zhiyuan.io/v1alpha1', kind: 'DeploymentConfig', metadata: metadata('deployment'), spec: {deploymentId: 'probe'}},
    db, object, control, ...['aep-business-db', 'aep-business-object', 'aep-business-control'].map(service),
  ]);
  for (const name of ['aep-business-db', 'aep-business-control']) {
    await kubectl(['-n', namespace, 'rollout', 'status', 'deployment/' + name, '--timeout=180s']);
  }
  const port = await forward('deployment/aep-business-control', 8080);
  await until(async () => assert.equal((await fetch('http://127.0.0.1:' + port + '/readyz', {signal: AbortSignal.timeout(5000)})).status, 200));
  return {issuer, baseUrl: 'http://127.0.0.1:' + port, password, controlImage};
}

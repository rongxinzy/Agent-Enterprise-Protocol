// Real employee Operator and trusted AEP observer; no test-written identity status.
import assert from 'node:assert/strict';
import {readFile, rm} from 'node:fs/promises';
import path from 'node:path';
import yaml from 'js-yaml';

export const employeeAudience = 'urn:zhiyuan:model-gateway:employee-acceptance';
export const employeeGatewayHost = 'higress-gateway.higress-system.svc';

export async function employeeBusinessStack(ctx, deploymentId) {
  const {command, kubectl, apply, until, work, cluster} = ctx;
  const directory = process.env.AEP_EMPLOYEE_GOVERNANCE_DIR;
  const operatorImage = process.env.AEP_EMPLOYEE_OPERATOR_IMAGE;
  const runtimeImage = process.env.AEP_EMPLOYEE_RUNTIME_IMAGE;
  assert(directory && operatorImage && runtimeImage, 'Explicit current employee source and built image references are required');
  const load = async (ref, name) => {
    const archive = path.join(work, name + '.tar');
    await command('docker', ['image', 'save', '--platform=linux/amd64', '-o', archive, ref]);
    await command(process.env.KIND ?? 'kind', ['load', 'image-archive', '--name', cluster, archive]);
    await rm(archive);
  };
  await load(operatorImage, 'employee-operator');
  await load(runtimeImage, 'employee-runtime');
  await apply([{apiVersion: 'v1', kind: 'Namespace', metadata: {name: 'digital-employees', labels: {
    'pod-security.kubernetes.io/enforce': 'restricted'}}}]);
  assert(ctx.governanceCredentials?.username && ctx.governanceCredentials?.password,
    'Disposable governance control account must be created through the business API');
  await apply([{apiVersion: 'v1', kind: 'Secret', metadata: {name: 'employee-governance', namespace: 'digital-employees'},
    stringData: {GOV_SERVICE_USERNAME: ctx.governanceCredentials.username,
      GOV_SERVICE_PASSWORD: ctx.governanceCredentials.password}}]);
  await kubectl(['apply', '-f', path.join(directory, 'operator/deploy/crd.yaml')]);
  for (const name of ['keda-scaledobjects.yaml', 'keda-triggerauthentications.yaml']) {
    assert(process.env.AEP_EMPLOYEE_KEDA_CRD_DIR, 'Explicit KEDA CRD directory is required');
    await kubectl(['apply', '--server-side', '-f', path.join(process.env.AEP_EMPLOYEE_KEDA_CRD_DIR, name)]);
  }
  await kubectl(['wait', '--for=condition=Established', 'crds', '--all', '--timeout=60s']);
  const manifest = await readFile(path.join(directory, 'operator/deploy/operator.yaml'), 'utf8');
  const objects = yaml.loadAll(manifest).filter(Boolean);
  for (const deployment of objects.filter(object => object.kind === 'Deployment')) {
    deployment.spec.template.spec.securityContext = {runAsNonRoot: true, runAsUser: 65532,
      runAsGroup: 65532, seccompProfile: {type: 'RuntimeDefault'}};
    const container = deployment.spec.template.spec.containers[0];
    container.image = operatorImage;
    container.securityContext = {allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: ['ALL']}};
  }
  await apply(objects);
  await kubectl(['-n', 'digital-employees', 'set', 'env', 'deployment/de-operator',
    'MODEL_ACCESS_TOKEN_AUDIENCE=' + employeeAudience, 'MODEL_ACCESS_GATEWAY_URL=http://' + employeeGatewayHost + '/v1',
    'RUNTIME_SECURITY_CONTEXT=' + JSON.stringify({runAsNonRoot: true, runAsUser: 1000, runAsGroup: 1000,
      allowPrivilegeEscalation: false, capabilities: {drop: ['ALL']}, seccompProfile: {type: 'RuntimeDefault'}})]);
  await kubectl(['-n', 'digital-employees', 'rollout', 'status', 'deployment/de-operator', '--timeout=150s']);
  const refs = path.join(work, 'employee-observer-image.txt');
  await command(process.env.KO ?? 'go', [...(process.env.KO ? [] : ['run', 'github.com/google/ko@v0.18.0']),
    'build', '--platform', 'linux/amd64', '--image-refs', refs, './services/control-service/cmd/workload-observer'],
  {commandEnv: {...process.env, KO_DOCKER_REPO: 'ko.local'}, timeout: 600_000});
  const observerImage = (await readFile(refs, 'utf8')).trim();
  await load(observerImage, 'employee-observer');
  await kubectl(['apply', '-f', 'deploy/kubernetes/workload-observer/rbac.yaml']);
  await apply([{apiVersion: 'apps/v1', kind: 'Deployment', metadata: {name: 'aep-workload-observer', namespace: 'aep-system'},
    spec: {replicas: 1, selector: {matchLabels: {app: 'aep-workload-observer'}}, template: {
      metadata: {labels: {app: 'aep-workload-observer'}}, spec: {serviceAccountName: 'aep-workload-observer',
        securityContext: {runAsNonRoot: true, runAsUser: 65532, runAsGroup: 65532, seccompProfile: {type: 'RuntimeDefault'}},
        containers: [{name: 'observer', image: observerImage, env: [
          {name: 'AEP_ENVIRONMENT', value: 'test'}, {name: 'AEP_WORKLOAD_DEPLOYMENT_ID', value: deploymentId},
          {name: 'AEP_WORKLOAD_NAMESPACE', value: 'digital-employees'}, {name: 'AEP_WORKLOAD_AUDIENCE', value: employeeAudience},
          {name: 'AEP_WORKLOAD_FRESHNESS', value: '40s'}, {name: 'AEP_WORKLOAD_DATABASE_URL', valueFrom: {
            secretKeyRef: {name: 'aep-business-control', key: 'AEP_DATABASE_URL'}}}],
          readinessProbe: {httpGet: {path: '/readyz', port: 8096}},
          securityContext: {allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: ['ALL']}}}]}}}}]);
  await kubectl(['-n', 'aep-system', 'rollout', 'status', 'deployment/aep-workload-observer', '--timeout=150s']);
  return {
    operatorImage, runtimeImage, observerImage,
    async create(name, agentId, ownerId, modelId) {
      await apply([{apiVersion: 'digitalemployee.zhiyuan.io/v1alpha1', kind: 'DigitalEmployee',
        metadata: {name, namespace: 'digital-employees'}, spec: {
          displayName: name, description: 'Isolated native employee model acceptance', owner: ownerId,
          runtime: {type: 'deerflow', image: runtimeImage, model: modelId, provider: 'openai'},
          governance: {aepBaseURL: 'http://aep-business-control.aep-system.svc:8080', deploymentID: deploymentId,
            defaultAEPUser: ownerId},
          credentials: {name: 'employee-governance'}, modelAccess: {agentId}, persistence: {enabled: false}, replicas: 1}}]);
      return until(async () => {
        const employee = JSON.parse(await kubectl(['-n', 'digital-employees', 'get', 'digitalemployee', name, '-o', 'json']));
        assert.equal(employee.status?.modelIdentity?.agentId, agentId);
        assert.equal(employee.status.modelIdentity.observedGeneration, employee.metadata.generation);
        assert.equal(employee.status.modelIdentity.employeeUid, employee.metadata.uid);
        return employee;
      });
    },
    async modelCall(name) {
      const script = `import json, os
import yaml
from deerflow.config.app_config import AppConfig
from deerflow.models.factory import create_chat_model
from openai import APIStatusError
app = AppConfig.model_validate(yaml.safe_load(open(os.environ['DEER_FLOW_CONFIG_PATH'])))
try:
    result = create_chat_model(app.models[0].name, app_config=app, attach_tracing=False).invoke('fixture-only')
    print(json.dumps({'status':200, 'content':result.content, 'usage':result.usage_metadata}))
except APIStatusError as error:
    print(json.dumps({'status':error.status_code}))
`;
      const output = await kubectl(['-n', 'digital-employees', 'exec', 'deployment/de-' + name, '-c', 'runtime', '--',
        '/app/backend/.venv/bin/python', '-c', script]);
      return JSON.parse(output.trim().split('\n').at(-1));
    },
  };
}

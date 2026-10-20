// Real employee CR/SA lifecycle -> trusted observer -> business publication -> gateway.
// No test-written identity status, grants, AuthConfigs or business database rows.
import assert from 'node:assert/strict';
import {randomBytes} from 'node:crypto';
import {AepClient, MemoryTokenStore} from '../../../packages/aep-sdk-node/dist/index.js';
import {businessStack} from './business-stack.mjs';
import {businessFleet} from './business-fleet.mjs';
import {resources} from './resources.mjs';
import {higressAccess} from './managed-routes.mjs';
import {employeeBusinessStack, employeeAudience, employeeGatewayHost} from './employee-business-stack.mjs';
import {trustedQuotaAcceptance} from './trusted-quota.mjs';

export async function employeeBusinessAcceptance(ctx) {
  const {kubectl, apply, until, get, forward, request, record, report, httpsSource} = ctx;
  assert(httpsSource, 'Employee business acceptance requires actual HTTPS policy delivery');
  assert.equal(report.requireEntitlement, true, 'Enterprise workload license enforcement must remain enabled');
  assert.equal(process.env.AEP_NATIVE_ACCESS_MANAGED, 'true');
  assert.equal(process.env.AEP_NATIVE_ACCESS_HIGRESS, 'true');
  const stack = await businessStack({...ctx, deploymentId: 'offline-e2e-deployment', licenseFixture: true});
  await kubectl(['-n', 'aep-system', 'set', 'env', 'deployment/aep-gateway-operator',
    'AEP_GATEWAY_ACCESS_OPERATOR_ENABLED=true', 'AEP_GATEWAY_ACCESS_ISSUER_URL=' + stack.issuer,
    'AEP_GATEWAY_ACCESS_POLICY_BASE_URL=' + httpsSource.baseURL, 'AEP_GATEWAY_ACCESS_REFRESH_SECONDS=1',
    'AEP_GATEWAY_ACCESS_REQUIRE_ENTITLEMENT=true', 'AEP_GATEWAY_ACCESS_WORKLOAD_AUDIENCE=' + employeeAudience,
    'AEP_GATEWAY_ACCESS_INGRESS_HOST=' + employeeGatewayHost]);
  await kubectl(['apply', '-k', 'services/gateway-operator/config/access-ingress-rbac']);
  const objects = JSON.parse(JSON.stringify(resources('')).replaceAll('auth-cache-probe', 'aep-system'))
    .filter(o => ['ServiceAccount', 'Role', 'RoleBinding'].includes(o.kind) ||
      o.kind === 'Deployment' && ['authorino-a', 'authorino-b'].includes(o.metadata.name));
  for (const deployment of objects.filter(o => o.kind === 'Deployment')) {
    const pod = deployment.spec.template;
    pod.metadata.labels['aep-acceptance-authorino'] = 'true';
    pod.metadata.labels['gateway.aep.zhiyuan.io/access-verifier'] = 'true';
    pod.spec.securityContext = {runAsNonRoot: true, runAsUser: 1000, fsGroup: 1000,
      seccompProfile: {type: 'RuntimeDefault'}};
    const container = pod.spec.containers[0];
    container.args = container.args.map(arg => arg === '--evaluator-cache-size=1' ? '--evaluator-cache-size=4' : arg);
  }
  httpsSource.configure(objects);
  const installed = objects.filter(o => o.metadata.name !== 'authorino-no-ca');
  for (const deployment of installed.filter(o => o.kind === 'Deployment')) {
    const pod = deployment.spec.template;
    pod.spec.volumes.push({name: 'tmp', emptyDir: {}});
    pod.spec.containers[0].securityContext.readOnlyRootFilesystem = true;
    pod.spec.containers[0].volumeMounts.push({name: 'tmp', mountPath: '/tmp'});
  }
  await apply(installed);
  await kubectl(['apply', '-k', 'deploy/kubernetes/access-workload-review']);
  for (const name of ['aep-gateway-operator', 'authorino-a', 'authorino-b']) {
    await kubectl(['-n', 'aep-system', 'rollout', 'status', 'deployment/' + name, '--timeout=150s']);
  }
  const admin = new AepClient({baseUrl: stack.baseUrl, tokenStore: new MemoryTokenStore()});
  await admin.loginWithPassword({deploymentId: stack.deploymentId, username: 'admin', password: stack.password});
  const roles = await Promise.all(['Employee A', 'Employee B', 'Owner only'].map(name =>
    admin.createRole({name, permissions: []})));
  const team = await admin.createTeam({name: 'Employee acceptance', defaultRoleIds: []});
  const peerTeam = await admin.createTeam({name: 'Employee peer acceptance', defaultRoleIds: []});
  const owner = await admin.createUser({deploymentId: stack.deploymentId, username: 'employee-owner',
    displayName: 'Employee owner', temporaryPassword: randomBytes(24).toString('hex'),
    requirePasswordChange: false, roleIds: [roles[2].id], teamIds: [team.id]});
  const providerKey = randomBytes(24).toString('hex');
  const credential = await admin.createCredential({name: 'Employee mock upstream', service: 'disposable',
    type: 'api_key', deliveryMode: 'server_only', value: providerKey, enabled: true});
  // Fixture delivery only: this scenario does not claim credential-publisher acceptance.
  await apply([
    {apiVersion: 'v1', kind: 'Secret', metadata: {name: 'aep-credential-' + credential.id, namespace: 'aep-credentials'},
      stringData: {'api-key': providerKey}},
    {apiVersion: 'v1', kind: 'Secret', metadata: {name: 'access-upstream-credential', namespace: 'aep-system'},
      stringData: {'api-key': providerKey}},
  ]);
  const model = await admin.createModel({displayName: 'Employee acceptance model', sourceType: 'gateway',
    protocol: 'openai-compatible', endpoint: 'http://access-upstream.aep-system.svc:8080/v1', credentialId: credential.id,
    upstreamModel: 'disposable-model', capabilities: ['text'], isDefault: false, enabled: true});
  for (const role of roles.slice(0, 2)) {
    await admin.createModelAssignment({modelId: model.id, subject: {type: 'role', id: role.id}});
  }
  await admin.createDataPlanePublication('employee-business-initial');
  const governanceCredentials = {username: 'employee-governance', password: randomBytes(24).toString('hex')};
  const governanceRole = await admin.createRole({name: 'Governance control only', permissions: []});
  const governanceTeam = await admin.createTeam({name: 'Governance control only', defaultRoleIds: []});
  await admin.createUser({deploymentId: stack.deploymentId, username: governanceCredentials.username,
    temporaryPassword: governanceCredentials.password, displayName: 'Governance control only', requirePasswordChange: false,
    roleIds: [governanceRole.id], teamIds: [governanceTeam.id]});
  const runtime = await employeeBusinessStack({...ctx, governanceCredentials}, stack.deploymentId);
  Object.assign(report, {controlImage: stack.controlImage, employeeImages: runtime,
    scope: 'real employee Operator/Pod -> trusted lifecycle observer -> business DB/outbox/relay -> CR -> Authorino/Higress -> mock provider',
    employeeBusinessVerified: false, employeeCloudVerified: false, credentialPublisherVerified: false,
    productionFleetObserverImplemented: false, adoptionGate: false});
  const employees = [];
  for (const [index, name] of ['native-employee-a', 'native-employee-b'].entries()) {
    const account = await admin.createAgent({username: name, displayName: name, password: randomBytes(24).toString('hex'),
      roleIds: [roles[index].id], homeTeamId: index === 0 ? team.id : peerTeam.id, teamIds: [], ephemeral: false});
    const employee = await runtime.create(name, account.id, owner.id, model.id);
    const identity = employee.status.modelIdentity;
    const grant = await until(async () => {
      const list = JSON.parse(await kubectl(['-n', 'aep-system', 'get', 'modelaccessgrants', '-o', 'json'])).items;
      const matches = list.filter(g => g.spec.subject.kind === 'agent' && g.spec.subject.id === account.id &&
        g.spec.binding.kind === 'kubernetes' && g.spec.binding.workload.employeeUid === employee.metadata.uid);
      assert.equal(matches.length, 1, 'Trusted lifecycle/publication has not produced exactly one employee binding');
      assert(!matches[0].spec.disabled && matches[0].spec.models.includes(model.id));
      assert.equal(matches[0].spec.workloadEntitlement.licenseId, 'lic-offline-e2e-v1');
      return matches[0];
    });
    await kubectl(['-n', 'digital-employees', 'rollout', 'status', 'deployment/de-' + name, '--timeout=180s']);
    const pods = JSON.parse(await kubectl(['-n', 'digital-employees', 'get', 'pods', '-o', 'json'])).items;
    const pod = pods.find(p => !p.metadata.deletionTimestamp && p.spec.serviceAccountName === identity.serviceAccountName);
    assert(pod && pod.status.conditions.some(c => c.type === 'Ready' && c.status === 'True'));
    const token = await kubectl(['-n', 'digital-employees', 'create', 'token', identity.serviceAccountName,
      '--audience=' + employeeAudience, '--duration=10m', '--bound-object-kind=Pod',
      '--bound-object-name=' + pod.metadata.name, '--bound-object-uid=' + pod.metadata.uid]);
    employees.push({name, user: account, binding: grant.metadata.annotations['gateway.aep.zhiyuan.io/access-binding'],
      tokens: {modelAccessToken: token}, employeeUid: employee.metadata.uid, serviceAccountUid: identity.serviceAccountUid});
  }
  const [alice, bob] = employees;
  const fleet = await until(() => businessFleet({kubectl, forward, request}));
  await until(() => fleet.check(alice, model.id, 200));
  await until(() => fleet.check(bob, model.id, 200));
  const reviews = async () => (await kubectl(['get', '--raw', '/metrics'])).split('\n')
    .filter(line => line.startsWith('apiserver_request_total{') && line.includes('resource="tokenreviews"') &&
      /verb="(?:CREATE|POST)"/.test(line))
    .reduce((total, line) => total + Number(line.slice(line.lastIndexOf(' ') + 1)), 0);
  const coldReviews = await reviews();
  assert(coldReviews >= 4, 'Both employee credentials must have been reviewed on both serving processes');
  for (let i = 0; i < 3; i++) {
    await fleet.check(alice, model.id, 200); await fleet.check(bob, model.id, 200);
  }
  assert.equal(await reviews(), coldReviews, 'Warm employee calls repeated TokenReview');
  report.workload = true;
  report.employeeWarmTokenReviewReuseVerified = true;
  record('Kubernetes API metrics witness cold employee/process authentication and zero additional reviews for repeated warm calls');
  record('Two real employee CRs/owned SAs produce licensed agent grants through the trusted observer and DB publication pipeline');
  const gateway = await higressAccess({...ctx, gatewayHost: employeeGatewayHost}, alice, bob, model);
  // Do not retry inference: convergence was established using verifier/gateway probes first.
  for (const employee of employees) {
    const result = await runtime.modelCall(employee.name);
    assert.equal(result.status, 200); assert.equal(result.content, 'probe'); assert.equal(result.usage.total_tokens, 2);
  }
  record('Actual employee runtime factory consumes Operator-generated config and projected Pod tokens; both employees infer through native Higress');
  const policy = (person, allowed) => until(async () => {
    const grant = await get('modelaccessgrant', 'access-' + person.binding);
    assert.equal(!grant.spec.disabled && grant.spec.models.includes(model.id), allowed);
    return grant;
  });
  const peer = await get('authconfig', 'access-' + bob.binding);
  const beforeAffected = await reviews();
  await admin.replaceUserRBAC(alice.user.id, {roleIds: [roles[2].id], teamIds: [team.id]});
  await policy(alice, false);
  await until(() => fleet.check(alice, model.id, 403));
  await until(() => gateway.check(alice, 403));
  assert.equal((await runtime.modelCall(alice.name)).status, 403);
  await gateway.check(bob, 200);
  assert.equal((await get('authconfig', 'access-' + bob.binding)).metadata.resourceVersion, peer.metadata.resourceVersion);
  const afterAffected = await reviews();
  assert(afterAffected > beforeAffected, 'Affected employee policy was not reauthenticated after publication');
  await fleet.check(bob, model.id, 200); await fleet.check(bob, model.id, 200);
  assert.equal(await reviews(), afterAffected, 'Unrelated employee lost its warm authentication');
  report.employeeReviewWitness = {coldReviews, beforeRoleChange: beforeAffected, afterRoleChange: afterAffected,
    afterPeerWarmCalls: await reviews()};
  await admin.replaceUserRBAC(alice.user.id, {roleIds: [roles[0].id], teamIds: [team.id]});
  await policy(alice, true); await until(() => gateway.check(alice, 200));
  assert.equal((await runtime.modelCall(alice.name)).status, 200);
  record('API employee role revoke/restore reaches the running Pod without restart; unrelated employee AuthConfig is unchanged');
  const ownPolicy = await get('authconfig', 'access-' + alice.binding);
  await admin.createModelAssignment({modelId: model.id, subject: {type: 'role', id: roles[2].id}});
  await admin.updateUser(owner.id, {status: 'disabled'});
  await until(() => fleet.check(alice, model.id, 200));
  await gateway.check(alice, 200); await gateway.check(bob, 200);
  assert.equal((await get('authconfig', 'access-' + alice.binding)).metadata.resourceVersion, ownPolicy.metadata.resourceVersion);
  record('Owner role grant and human owner disablement do not lend or withdraw employee model identity');
  await admin.updateTeam(team.id, {defaultRoleIds: [roles[0].id]});
  await trustedQuotaAcceptance({...ctx, admin, gateway, alice, bob, model});
  for (const kind of ['requests', 'tokens']) {
    const rule = await admin.putGatewayLimit('employee-pod-' + kind, {kind, scopeType: 'user', scopeId: alice.user.id,
      modelId: model.id, maximum: 1, interval: 'minute', enabled: true, expectedVersion: 0});
    await gateway.nativeRedis.publication();
    const name = await until(() => gateway.nativeRedis.rulePlugin(rule));
    await until(() => gateway.nativeRedis.loaded(name));
    await gateway.nativeRedis.redis('FLUSHDB');
    assert.equal((await runtime.modelCall(alice.name)).status, 200);
    if (kind === 'tokens') await until(async () => {
      const keys = (await gateway.nativeRedis.redis('--scan', '--pattern', 'higress-token-ratelimit*')).split(/\r?\n/).filter(Boolean);
      assert.equal(keys.length, 1);
      assert.equal(await gateway.nativeRedis.redis('GET', keys[0]), '2', 'Native plugin must account for actual mock usage');
    });
    const before = await gateway.stats();
    assert.equal((await runtime.modelCall(alice.name)).status, 429);
    assert.equal(await gateway.stats(), before, 'Limited employee Pod request reached the provider');
    assert.equal((await runtime.modelCall(bob.name)).status, 200, 'Unrelated employee must retain its own quota');
    await admin.deleteGatewayLimit(rule.id, rule.version);
    await gateway.nativeRedis.publication();
    await until(() => gateway.check(alice, 200));
    record('Actual employee Pod ' + kind + ' limit returns 429 before upstream while peer remains allowed');
  }
  report.employeeBusinessVerified = true;
  report.employeePodQuotaVerified = true;
  report.scope = 'Employee provisioning/policy: API -> DB/outbox/relay -> CR/Operator; inference: actual Pod -> native Authorino/Higress -> mock provider + Redis';
  report.employeeSubjects = employees.map(({user, employeeUid, serviceAccountUid}) => ({agentId: user.id, employeeUid, serviceAccountUid}));
  report.enumeratedVerifierProcesses = fleet.identities();
  report.adoptionGate = false;
}

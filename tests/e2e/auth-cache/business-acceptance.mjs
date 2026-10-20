// Real business API -> PostgreSQL/outbox/relay -> CR -> Operator -> Authorino.
// No direct fixture SQL/CR edits simulate business authorization mutations.
import assert from 'node:assert/strict';
import {createHash, randomBytes} from 'node:crypto';
import {AepClient, MemoryTokenStore} from '../../../packages/aep-sdk-node/dist/index.js';
import {resources} from './resources.mjs';
import {businessStack} from './business-stack.mjs';
import {businessFleet} from './business-fleet.mjs';
import {higressAccess as unmanagedAccess} from './higress-access.mjs';
import {higressAccess as managedAccess} from './managed-routes.mjs';

export async function businessAcceptance(context) {
  const {command, kubectl, apply, forward, until, request, get, record, httpsSource, report} = context;
  assert(httpsSource, 'Business acceptance requires the actual HTTPS source deployment');
  assert.equal(report.requireEntitlement, false, 'Business acceptance currently covers model JWTs, not enterprise entitlements');
  const managed = process.env.AEP_NATIVE_ACCESS_MANAGED === 'true';
  assert(!managed || process.env.AEP_NATIVE_ACCESS_HIGRESS === 'true', 'Managed routes require real Higress');
  const stack = await businessStack(context);
  report.scope = 'real business API -> DB/outbox/relay -> CR/Operator -> enumerated native processes; no Higress/provider/workload';
  report.controlImage = stack.controlImage;
  report.productionFleetObserverImplemented = false;
  report.cacheHitWitness = false;
  await kubectl(['-n', 'aep-system', 'set', 'env', 'deployment/aep-gateway-operator',
    'AEP_GATEWAY_ACCESS_OPERATOR_ENABLED=true', 'AEP_GATEWAY_ACCESS_ISSUER_URL=' + stack.issuer,
    'AEP_GATEWAY_ACCESS_POLICY_BASE_URL=' + httpsSource.baseURL, 'AEP_GATEWAY_ACCESS_REFRESH_SECONDS=1',
    'AEP_GATEWAY_ACCESS_REQUIRE_ENTITLEMENT=false', ...(managed ? ['AEP_GATEWAY_ACCESS_INGRESS_HOST=shared.access.test'] : [])]);
  if (managed) await kubectl(['apply', '-k', 'services/gateway-operator/config/access-ingress-rbac']);
  const objects = JSON.parse(JSON.stringify(resources('')).replaceAll('auth-cache-probe', 'aep-system'))
    .filter(o => ['ServiceAccount', 'Role', 'RoleBinding'].includes(o.kind) || o.kind === 'Deployment' && o.metadata.name.startsWith('authorino-'));
  for (const deployment of objects.filter(o => o.kind === 'Deployment')) {
    deployment.spec.template.spec.containers[0].args = deployment.spec.template.spec.containers[0].args
      .map(arg => arg === '--evaluator-cache-size=1' ? '--evaluator-cache-size=4' : arg);
  }
  httpsSource.configure(objects);
  const installed = objects.filter(o => o.metadata.name !== 'authorino-no-ca');
  for (const deployment of installed.filter(o => o.kind === 'Deployment')) {
    const pod = deployment.spec.template;
    pod.metadata.labels['aep-acceptance-authorino'] = 'true';
    pod.metadata.labels['gateway.aep.zhiyuan.io/access-verifier'] = 'true';
    pod.spec.securityContext = {runAsNonRoot: true, runAsUser: 1000, fsGroup: 1000, seccompProfile: {type: 'RuntimeDefault'}};
    pod.spec.volumes.push({name: 'tmp', emptyDir: {}});
    pod.spec.containers[0].securityContext.readOnlyRootFilesystem = true;
    pod.spec.containers[0].volumeMounts.push({name: 'tmp', mountPath: '/tmp'});
  }
  await apply(installed);
  for (const name of ['aep-gateway-operator', 'authorino-a', 'authorino-b']) {
    await kubectl(['-n', 'aep-system', 'rollout', 'status', 'deployment/' + name, '--timeout=150s']);
  }
  const adminStore = new MemoryTokenStore();
  let admin = new AepClient({baseUrl: stack.baseUrl, tokenStore: adminStore});
  await admin.loginWithPassword({deploymentId: 'probe', username: 'admin', password: stack.password});
  const aliceRole = await admin.createRole({name: 'Acceptance Alice', permissions: []});
  const bobRole = await admin.createRole({name: 'Acceptance Bob', permissions: []});
  const basicRole = await admin.createRole({name: 'Acceptance basic', permissions: []});
  const team = await admin.createTeam({name: 'Acceptance team', defaultRoleIds: [aliceRole.id]});
  const otherTeam = await admin.createTeam({name: 'Acceptance unrelated team', defaultRoleIds: []});
  let credential;
  if (managed) {
    const providerKey = randomBytes(24).toString('hex');
    credential = await admin.createCredential({name: 'Managed route mock', service: 'disposable', type: 'api_key', deliveryMode: 'server_only', value: providerKey, enabled: true});
    await apply([
      {apiVersion: 'v1', kind: 'Secret', metadata: {name: 'aep-credential-' + credential.id, namespace: 'aep-credentials'}, stringData: {'api-key': providerKey}},
      {apiVersion: 'v1', kind: 'Secret', metadata: {name: 'access-upstream-credential', namespace: 'aep-system'}, stringData: {'api-key': providerKey}},
    ]);
  }
  const model = await admin.createModel({displayName: 'Acceptance model', sourceType: 'gateway', protocol: 'openai-compatible',
    endpoint: managed ? 'http://access-upstream.aep-system.svc:8080/v1' : 'https://disposable-provider.invalid/v1',
    ...(credential ? {credentialId: credential.id} : {}), upstreamModel: 'disposable-model', capabilities: ['text'], isDefault: false, enabled: true});
  if (managed) await admin.createDataPlanePublication('managed-routes-one');
  await admin.createModelAssignment({modelId: model.id, subject: {type: 'role', id: aliceRole.id}});
  const bobAssignment = await admin.createModelAssignment({modelId: model.id, subject: {type: 'role', id: bobRole.id}});
  const people = {};
  for (const [username, roleIds, teamIds] of [['alice', [basicRole.id], [team.id]], ['bob', [bobRole.id], [otherTeam.id]]]) {
    const password = randomBytes(24).toString('hex');
    const user = await admin.createUser({deploymentId: 'probe', username, displayName: username,
      temporaryPassword: password, requirePasswordChange: false, roleIds, teamIds});
    people[username] = {user, password};
  }
  async function login(account) {
    const store = new MemoryTokenStore();
    const client = new AepClient({baseUrl: stack.baseUrl, tokenStore: store});
    const tokens = await client.loginWithPassword({deploymentId: 'probe', username: account.user.username, password: account.password});
    const binding = createHash('sha256').update(JSON.stringify({deploymentId: 'probe', subject: {kind: 'human', id: account.user.id},
      binding: {kind: 'session', sessionId: tokens.sessionId}})).digest('hex');
    return {tokens, binding, client, store, user: account.user};
  }
  const alice = await login(people.alice), bob = await login(people.bob);
  const fleet = await until(async () => businessFleet({kubectl, forward, request}));
  const policy = (person, test) => until(async () => {
    const value = await get('modelaccessgrant', 'access-' + person.binding);
    assert.equal(value.spec.subject.id, person.user.id);
    assert.equal(value.spec.binding.sessionId, person.tokens.sessionId);
    assert(test(value.spec), 'Business mutation has not reached its CR');
    return value;
  });
  let gateway;
  const enforcement = (person, status, id = model.id) => until(async () => {
    const result = await fleet.check(person, id, status);
    if (gateway) await gateway.check(person, status, id);
    return result;
  });
  const allowed = spec => !spec.disabled && spec.models.includes(model.id);
  await policy(alice, allowed); await policy(bob, allowed);
  if (process.env.AEP_NATIVE_ACCESS_HIGRESS === 'true') {
    gateway = await (managed ? managedAccess : unmanagedAccess)(context, alice, bob, model);
    if (managed) {
      const {trustedQuotaAcceptance} = await import('./trusted-quota.mjs');
      await trustedQuotaAcceptance({...context, admin, gateway, alice, bob, model});
    }
  }
  if (process.env.AEP_NATIVE_ACCESS_QUOTA_ADMIN === 'true') {
    assert(managed, 'Quota management acceptance requires the managed native path');
    const {quotaManagementProbe} = await import('./quota-management-probe.mjs');
    await quotaManagementProbe({...context, admin, adminStore, gateway, person: bob});
    report.scope += '; focused native quota management probe, not the full session mutation/recovery baseline';
    return;
  }
  const initial = await enforcement(alice, 200); await enforcement(bob, 200);
  assert.deepEqual((await alice.client.listModels()).models.map(m => m.id), [model.id]);
  record('Real SDK/API role grants, team defaults and password sessions reach both independently enumerated native processes', {processes: fleet.identities(), observations: initial});
  await enforcement(alice, 403, 'unassigned-model');
  await until(async () => fleet.check({...alice, binding: bob.binding}, model.id, 403));
  record('Actual-body model escalation and cross-session configuration selection deny with real service-issued JWTs');

  const bobNative = await get('authconfig', 'access-' + bob.binding);
  await admin.updateTeam(team.id, {defaultRoleIds: []});
  await policy(alice, spec => !spec.models.includes(model.id)); await enforcement(alice, 403); await enforcement(bob, 200);
  assert.equal((await get('authconfig', 'access-' + bob.binding)).metadata.resourceVersion, bobNative.metadata.resourceVersion);
  await admin.updateTeam(team.id, {defaultRoleIds: [aliceRole.id]});
  await policy(alice, allowed); await enforcement(alice, 200);
  record('Changing a team default role withdraws and restores Alice; unrelated Bob native configuration stays unchanged');

  async function cycle(name, person, unrelated, mutate, restore) {
    const untouched = await get('authconfig', 'access-' + unrelated.binding);
    await mutate();
    await policy(person, spec => !spec.models.includes(model.id));
    await enforcement(person, 403); await enforcement(unrelated, 200);
    assert.equal((await get('authconfig', 'access-' + unrelated.binding)).metadata.resourceVersion, untouched.metadata.resourceVersion);
    await restore();
    await policy(person, allowed); await enforcement(person, 200);
    record(name);
  }
  await cycle('Replacing direct user roles withdraws and restores Bob without rewriting unrelated Alice configuration', bob, alice,
    () => admin.replaceUserRBAC(bob.user.id, {roleIds: [basicRole.id], teamIds: [otherTeam.id]}),
    () => admin.replaceUserRBAC(bob.user.id, {roleIds: [bobRole.id], teamIds: [otherTeam.id]}));
  await cycle('Replacing team membership withdraws and restores inherited access without rewriting unrelated configuration', alice, bob,
    () => admin.replaceUserRBAC(alice.user.id, {roleIds: [basicRole.id], teamIds: [otherTeam.id]}),
    () => admin.replaceUserRBAC(alice.user.id, {roleIds: [basicRole.id], teamIds: [team.id]}));
  await cycle('Role disable/re-enable affects its member without rewriting unrelated configuration', alice, bob,
    () => admin.updateRole(aliceRole.id, {enabled: false}), () => admin.updateRole(aliceRole.id, {enabled: true}));
  await cycle('Team disable/re-enable withdraws and restores inherited access without rewriting unrelated configuration', alice, bob,
    () => admin.updateTeam(team.id, {enabled: false}), () => admin.updateTeam(team.id, {enabled: true}));
  await cycle('Deleting and recreating a role model assignment changes only the affected native configuration', bob, alice,
    () => admin.deleteModelAssignment(bobAssignment.id),
    () => admin.createModelAssignment({modelId: model.id, subject: {type: 'role', id: bobRole.id}}));

  const secondAlice = await login(people.alice);
  await policy(secondAlice, allowed); await enforcement(secondAlice, 200);
  await admin.revokeUserSession(alice.tokens.sessionId);
  await policy(alice, spec => spec.disabled && spec.models.length === 0); await enforcement(alice, 403);
  await enforcement(secondAlice, 200); await enforcement(bob, 200);
  record('Revoking one real login session retires its binding without revoking another Alice session or Bob');

  await admin.updateUser(people.alice.user.id, {status: 'disabled'});
  await policy(secondAlice, spec => spec.disabled); await enforcement(secondAlice, 403); await enforcement(bob, 200);
  await admin.updateUser(people.alice.user.id, {status: 'active'});
  const activeAlice = await login(people.alice);
  await policy(activeAlice, allowed); await enforcement(activeAlice, 200); await enforcement(alice, 403);
  record('Account disablement reaches both processes; re-enable/new login does not resurrect a retired session');

  await admin.updateModel(model.id, {enabled: false});
  await policy(activeAlice, spec => !spec.models.includes(model.id)); await policy(bob, spec => !spec.models.includes(model.id));
  await enforcement(activeAlice, 403); await enforcement(bob, 403);
  await admin.updateModel(model.id, {enabled: true});
  await policy(activeAlice, allowed); await policy(bob, allowed); await enforcement(activeAlice, 200); await enforcement(bob, 200);
  record('Catalog model disable/re-enable propagates via the real DB pipeline to every affected session');

  // Read-only ledger inspection correlates source publication, durable delivery
  // and native process evidence; business state is changed only by public APIs.
  const sql = `SELECT coalesce(json_agg(json_build_object('binding',p.binding_key,'sequence',p.sequence,'operation',p.operation_id,'hash',p.content_hash,'phase',p.phase)),'[]')
FROM model_access_streams s JOIN model_access_publications p ON p.deployment_id=s.deployment_id AND p.binding_key=s.binding_key AND p.sequence=s.last_sequence
WHERE s.deployment_id='probe'`;
  for (const person of [activeAlice, bob]) await until(async () => {
    const rows = JSON.parse(await kubectl(['-n', 'aep-system', 'exec', 'deployment/aep-business-db', '--', 'psql', '-U', 'aep', '-d', 'aep', '-tAc', sql]));
    const grant = await get('modelaccessgrant', 'access-' + person.binding);
    const row = rows.find(item => item.binding === person.binding);
    assert(row && row.phase === 'projected');
    assert.equal(row.operation, grant.spec.publication.operationId); assert.equal(row.hash, grant.spec.publication.contentHash);
    assert.equal(row.sequence, grant.spec.publication.sequence);
    await fleet.check(person, model.id, 200, grant.spec.publication);
  });
  record('Read-only DB ledger, CR publication and observations from every discovered native process correlate');

  const beforeOutage = await get('authconfig', 'access-' + bob.binding);
  await policy(bob, allowed);
  await kubectl(['-n', 'aep-system', 'scale', 'deployment/aep-business-control', '--replicas=0']);
  await until(async () => {
    const pods = JSON.parse(await kubectl(['-n', 'aep-system', 'get', 'pods', '-l', 'app=aep-business-control', '-o', 'json'])).items;
    assert.equal(pods.length, 0);
  });
  const stopped = await get('modelaccessgrant', 'access-' + bob.binding);
  const oldDeadline = Date.parse(stopped.spec.validUntil);
  const tokenExpiry = JSON.parse(Buffer.from(bob.tokens.modelAccessToken.split('.')[1], 'base64url')).exp * 1000;
  assert(tokenExpiry > oldDeadline + 30_000, 'JWT expiry must not substitute for publication expiry');
  assert(Date.now() < oldDeadline - 5000, 'Control shutdown exceeded the last publication lifetime');
  await enforcement(bob, 200);
  record('Both warmed native processes authorize within the published lifetime while the actual control/DB request path is unavailable');
  await until(async () => assert(Date.now() > oldDeadline + 1500));
  await enforcement(bob, 403); await enforcement(activeAlice, 403);
  record('Stopping the actual control/relay process causes warm permissions to deny after their fixed publication deadline');
  await kubectl(['-n', 'aep-system', 'scale', 'deployment/aep-business-control', '--replicas=1']);
  await kubectl(['-n', 'aep-system', 'rollout', 'status', 'deployment/aep-business-control', '--timeout=180s']);
  // Deployment port-forwards are bound to the old Pod and end on shutdown.
  // Preserve sessions, but reconnect SDK clients to the replacement Pod before
  // further business API calls. This is local test plumbing, not token renewal.
  const resumedPort = await forward('deployment/aep-business-control', 8080);
  stack.baseUrl = 'http://127.0.0.1:' + resumedPort;
  admin = new AepClient({baseUrl: stack.baseUrl, tokenStore: adminStore});
  for (const person of [alice, bob, secondAlice, activeAlice]) {
    person.client = new AepClient({baseUrl: stack.baseUrl, tokenStore: person.store});
  }
  await policy(bob, spec => allowed(spec) && Date.parse(spec.validUntil) > oldDeadline);
  await enforcement(bob, 200); await enforcement(activeAlice, 200); await enforcement(alice, 403);
  assert.equal((await get('authconfig', 'access-' + bob.binding)).metadata.resourceVersion, beforeOutage.metadata.resourceVersion);
  record('Restarted real control/relay rederives current DB grants without rewriting unchanged native AuthConfig or restoring retired sessions');

  const before = fleet.identities();
  const pods = JSON.parse(await kubectl(['-n', 'aep-system', 'get', 'pods', '-l', 'app=authorino-b', '-o', 'json'])).items;
  assert.equal(pods.length, 1);
  await kubectl(['-n', 'aep-system', 'delete', 'pod', pods[0].metadata.name, '--wait=true']);
  await kubectl(['-n', 'aep-system', 'rollout', 'status', 'deployment/authorino-b', '--timeout=150s']);
  await until(async () => fleet.refresh());
  assert.notDeepEqual(fleet.identities(), before);
  await enforcement(bob, 200); await enforcement(activeAlice, 200); await enforcement(alice, 403);
  const final = await get('modelaccessgrant', 'access-' + bob.binding);
  assert.notEqual(final.status?.conditions?.find(c => c.type === 'Ready')?.status, 'True');
  assert.equal(final.status?.replicas?.length ?? 0, 0);
  record('Rediscovery observes the new native process incarnation after restart; current allow/retired deny recover and no product Ready evidence is fabricated', {processes: fleet.identities()});
  if (gateway) await gateway.outage(bob);
  if (context.realProviderKey) {
    // The outage scenario replaces both verifier Pods. Capture their current
    // process identities and port-forwards before checking the cloud grant.
    await until(() => fleet.refresh());
    const {realProviderAcceptance} = await import('./real-provider.mjs');
    await realProviderAcceptance({...context, admin, gateway, fleet, person: bob, roleId: bobRole.id});
  }
}

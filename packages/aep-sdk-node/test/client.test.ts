import {afterEach, beforeEach, describe, expect, test} from 'vitest';

import {AepClient, AepProblem, FetchTransport, MemoryTokenStore} from '../src/index.js';
import {MockAepServer} from './mock-server.js';

describe('AepClient SDK gate', () => {
  let server: MockAepServer;
  let store: MemoryTokenStore;
  let client: AepClient;

  beforeEach(async () => {
    server = new MockAepServer();
    await server.start();
    store = new MemoryTokenStore();
    client = new AepClient({
      baseUrl: server.baseUrl,
      tokenStore: store,
      transport: new FetchTransport({defaultTimeoutMs: 2_000, maxRetries: 2}),
    });
  });

  afterEach(async () => {
    await server.stop();
  });

  test('adds protocol headers and retries safe requests', async () => {
    server.failMetadata(1);
    const metadata = await client.getMetadata();
    expect(metadata.capabilities).toContain('skills');
    const request = server.requests.at(-1);
    expect(request?.headers['x-aep-agent-id']).toBeUndefined();
    expect(request?.headers['x-aep-protocol-version']).toBe('1.0');
    expect(request?.headers['x-request-id']).toBeTruthy();
    expect(server.requests.filter(item => item.path === '/aep/v1/metadata')).toHaveLength(2);
  });

  test('sends requests when Web Crypto is unavailable', async () => {
    const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'crypto');
    Object.defineProperty(globalThis, 'crypto', {configurable: true, value: undefined});
    try {
      await client.getMetadata();
      expect(server.requests.at(-1)?.headers['x-request-id']).toMatch(
        /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
      );
    } finally {
      if (descriptor) Object.defineProperty(globalThis, 'crypto', descriptor);
      else delete (globalThis as {crypto?: unknown}).crypto;
    }
  });

  test('stores login tokens and loads current identity', async () => {
    const tokens = await client.loginWithPassword({deploymentId: 'ent-1', username: 'demo', password: 'password'});
    expect((await store.get())?.accessToken).toBe(tokens.accessToken);
    expect((await client.getCurrentIdentity()).user.id).toBe('user-1');
  });

  test('supports deployment-only password login during tenant migration', async () => {
    await expect(client.loginWithPassword({deploymentId: 'ent-1', username: 'demo', password: 'password'})).resolves.toBeTruthy();
    expect(server.requests.at(-1)?.path).toBe('/aep/v1/auth/password/login');
  });

  test('supports user-session clients without Agent headers', async () => {
    const sessionClient = new AepClient({
      baseUrl: server.baseUrl,
      tokenStore: store,
      transport: new FetchTransport({defaultTimeoutMs: 2_000, maxRetries: 2}),
    });
    const tokens = await sessionClient.loginWithPassword({deploymentId: 'ent-1', username: 'demo', password: 'password'});
    expect(tokens.sessionId).toBe('session-1');
    expect(server.requests.at(-1)?.headers['x-aep-agent-id']).toBeUndefined();
  });

  test('exposes canonical user-session discovery methods', async () => {
    const sessionClient = new AepClient({
      baseUrl: server.baseUrl,
      tokenStore: store,
      transport: new FetchTransport({defaultTimeoutMs: 2_000, maxRetries: 2}),
    });
    await sessionClient.loginWithPassword({deploymentId: 'ent-1', username: 'demo', password: 'password'});
    await sessionClient.getCurrentUser();
    expect(server.requests.at(-1)?.path).toBe('/aep/v1/user/me');
    await sessionClient.listModels();
    expect(server.requests.at(-1)?.path).toBe('/aep/v1/user/models');
  });

  test('activates a locally verified enterprise license', async () => {
    await client.loginWithPassword({deploymentId: 'ent-1', username: 'demo', password: 'password'});
    await expect(client.activateEnterpriseLicense({
      license: {
        format: 'zhiyuan-license-v1',
        keyId: 'license-prod-1',
        payload: {licenseId: 'lic-1', deploymentId: 'deployment-1'},
        signature: 'signed-license',
      },
    })).resolves.toMatchObject({entitlementToken: 'entitlement-1', tokenType: 'Bearer'});
    expect(server.requests.at(-1)?.path).toBe('/aep/v1/user/activation');
  });

  test('discovers models and returns an OpenAI-compatible gateway connection', async () => {
    await expect(client.getModelConnection()).rejects.toMatchObject({code: 'NO_SESSION', status: 401});
    await client.loginWithPassword({deploymentId: 'ent-1', username: 'demo', password: 'password'});

    expect((await client.listModels()).models[0]).toMatchObject({
      id: 'model-1',
      protocol: 'openai-compatible',
      upstreamModel: 'qwen3-32b',
    });
    await expect(client.getModelConnection()).resolves.toEqual({
      baseUrl: `${server.baseUrl}/openai/v1`,
      protocol: 'openai-compatible',
      apiVersion: 'v1',
      apiKey: 'model-1',
      expiresIn: 300,
    });

    server.disableModelGateway();
    await expect(client.getModelConnection()).rejects.toMatchObject({
      code: 'CAPABILITY_NOT_SUPPORTED',
      status: 501,
    });
  });

  test('covers model administration APIs', async () => {
    await client.loginWithPassword({deploymentId: 'ent-1', username: 'demo', password: 'password'});
    const write = {
      id: 'model-1',
      displayName: 'Enterprise Model',
      sourceType: 'gateway' as const,
      protocol: 'openai-compatible' as const,
      endpoint: '/openai/v1',
      upstreamModel: 'qwen3-32b',
      capabilities: ['text', 'tools', 'streaming', 'reasoning'],
      reasoningCompatibility: {
        thinkingFormat: 'deepseek' as const,
        supportsReasoningEffort: true as const,
        requiresReasoningContentOnAssistantMessages: true as const,
      },
      contextWindow: 131072,
      isDefault: true,
      enabled: true,
      credentialId: null,
    };

    expect((await client.listAdminModels()).models).toHaveLength(1);
    await expect(client.listModels()).resolves.toMatchObject({models: [{reasoningCompatibility: {thinkingFormat: 'deepseek'}}]});
    await expect(client.createModel(write)).resolves.toMatchObject({id: 'model-1'});
    await expect(client.getModel('model-1')).resolves.toMatchObject({id: 'model-1'});
    await expect(client.updateModel('model-1', {enabled: false})).resolves.toMatchObject({id: 'model-1'});
    await client.deleteModel('model-1');

    expect((await client.listModelAssignments()).assignments).toHaveLength(1);
    const subjectTypes = ['user', 'role', 'team'] as const;
    for (const type of subjectTypes) {
      await expect(client.createModelAssignment({modelId: 'model-1', subject: {type, id: type + '-1'}})).resolves.toMatchObject({id: 'assignment-1'});
    }
    await client.deleteModelAssignment('assignment-1');
  });

  test('withdraws a Skill version through the admin API', async () => {
    await client.loginWithPassword({deploymentId: 'ent-1', username: 'demo', password: 'password'});
    await expect(client.deleteSkillVersion('review', '1.0.0')).resolves.toBeUndefined();
    expect(server.requests.at(-1)?.path).toBe('/aep/v1/admin/skills/review/versions/1.0.0');
    expect(server.requests.at(-1)?.method).toBe('DELETE');
  });

  test('publishes and observes data-plane desired state without secret values', async () => {
    await client.loginWithPassword({deploymentId: 'ent-1', username: 'demo', password: 'password'});
    const desired = await client.getDataPlaneDesiredState();
    expect(desired.revision).toBe('rev-1');
    expect(desired.routes[0].providerType).toBe('deepseek');
    expect(desired.routes[0].credentialRef).toEqual({name: 'provider-secrets', key: 'model-1'});
    expect(JSON.stringify(desired)).not.toContain('provider-secret-value');
    await expect(client.putDataPlaneDesiredState({
      revision: 'rev-next',
      routes: [{modelId: 'model-1', enabled: true, endpoint: '/v1', upstreamModel: 'qwen3-32b', protocol: 'openai-compatible', providerType: 'deepseek', credentialRef: {name: 'provider-secrets', key: 'model-1'}}],
    })).resolves.toMatchObject({revision: 'rev-published'});
    await expect(client.getDataPlaneStatus()).resolves.toMatchObject({state: 'ready', observedRevision: 'rev-published', resourceCount: 1});
  });

  test('covers Credential delivery and administration without caching secrets', async () => {
    await client.loginWithPassword({deploymentId: 'ent-1', username: 'demo', password: 'password'});

    expect((await client.listCredentialsForUser()).credentials).toEqual([
      expect.objectContaining({id: 'credential-1', deliveryMode: 'client', maskedValue: '****cdef'}),
    ]);
    await expect(client.resolveCredentialForUser('credential-1', 'Call the example service')).resolves.toEqual({
      credentialId: 'credential-1',
      type: 'api_key',
      value: 'resolved-secret',
      expiresAt: null,
    });
    await expect(client.resolveCredentialForUser('server-only', 'Call the gateway')).rejects.toMatchObject({
      status: 403,
      code: 'CREDENTIAL_NOT_DELIVERABLE',
    });

    expect((await client.listCredentials()).credentials).toHaveLength(2);
    const input = {
      name: 'Agent API key',
      service: 'example-service',
      type: 'api_key' as const,
      deliveryMode: 'client' as const,
      value: 'new-secret',
      enabled: true,
    };
    await expect(client.createCredential(input)).resolves.toMatchObject({id: 'credential-1'});
    await expect(client.getCredential('credential-1')).resolves.toMatchObject({maskedValue: '****cdef'});
    await expect(client.updateCredential('credential-1', {enabled: false})).resolves.toMatchObject({id: 'credential-1'});
    await expect(client.rotateCredential('credential-1', {value: 'rotated-secret'})).resolves.toMatchObject({id: 'credential-1'});
    await client.deleteCredential('credential-1');
    await expect(client.deleteCredential('credential-in-use')).rejects.toMatchObject({status: 409, code: 'CREDENTIAL_IN_USE'});

    expect((await client.listCredentialAssignments()).assignments).toHaveLength(1);
    const subjectTypes = ['user', 'role', 'team'] as const;
    for (const type of subjectTypes) {
      await expect(
        client.createCredentialAssignment({
          credentialId: 'credential-1',
          subject: {type, id: type + '-1'},
        }),
      ).resolves.toMatchObject({resourceType: 'credential'});
    }
    await client.deleteCredentialAssignment('credential-assignment-1');

    server.omitCredentialNoStore();
    await expect(client.resolveCredentialForUser('credential-1', 'Unsafe response')).rejects.toMatchObject({
      status: 502,
      code: 'CREDENTIAL_RESPONSE_CACHEABLE',
    });
  });

  test('single-flights concurrent 401 refreshes', async () => {
    await client.loginWithPassword({deploymentId: 'ent-1', username: 'demo', password: 'password'});
    server.delayUnauthorizedResponses([0, 0, 0, 0, 50, 50, 50, 50]);
    server.expireAccessToken();
    const identities = await Promise.all(Array.from({length: 8}, () => client.getCurrentIdentity()));
    expect(identities).toHaveLength(8);
    expect(server.refreshCount).toBe(1);
    expect((await store.get())?.refreshToken).toBe('refresh-2');
  });

  test('clears the session when refresh is rejected', async () => {
    await client.loginWithPassword({deploymentId: 'ent-1', username: 'demo', password: 'password'});
    server.expireAccessToken();
    server.failRefresh();
    await expect(client.getCurrentIdentity()).rejects.toMatchObject({code: 'REFRESH_TOKEN_INVALID'});
    expect(await store.get()).toBeNull();
  });

  test('supports ETag manifests and binary package downloads', async () => {
    await client.loginWithPassword({deploymentId: 'ent-1', username: 'demo', password: 'password'});
    const first = await client.getUserSkillManifest();
    expect(first.notModified).toBe(false);
    const second = await client.getUserSkillManifest('"skills-1"');
    expect(second).toEqual({notModified: true, etag: '"skills-1"'});
    const archive = await client.downloadUserSkillPackage('review', '1.0.0');
    expect([...archive]).toEqual([0x50, 0x4b, 0x03, 0x04]);
  });

  test('maps RFC 9457 responses to AepProblem', async () => {
    await client.loginWithPassword({deploymentId: 'ent-1', username: 'demo', password: 'password'});
    const failure = client.getModel('missing');
    await expect(failure).rejects.toBeInstanceOf(AepProblem);
    await expect(failure).rejects.toMatchObject({status: 404, code: 'RESOURCE_NOT_FOUND', requestId: 'req-mock'});
  });

  test('covers control, telemetry, and administration APIs', async () => {
    await client.loginWithPassword({deploymentId: 'ent-1', username: 'demo', password: 'password'});
    expect((await client.heartbeatUser({clientVersion: '0.1.0', platform: 'windows'})).hasPendingControlEvents).toBe(true);
    expect((await client.listControlEvents()).items).toEqual([]);
    await client.acknowledgeControlEvent('delivery-1', '2026-08-20T00:00:00Z');
    await client.reportControlEventResult('delivery-1', {status: 'succeeded', completedAt: '2026-08-20T00:00:01Z'});
    expect(await client.uploadEventBatch([{eventId: 'event-1', type: 'auth.login'}])).toMatchObject({accepted: ['event-1']});
    expect((await client.listUsers()).items).toEqual([]);
  });

  test('covers RBAC, control-event, and license administration APIs', async () => {
    await client.loginWithPassword({deploymentId: 'ent-1', username: 'demo', password: 'password'});
    expect((await client.listPermissions()).permissions[0]?.id).toBe('models.read');
    expect((await client.listRoles()).roles[0]?.id).toBe('operator');
    expect((await client.createRole({id: 'operator', name: 'Operator'})).id).toBe('operator');
    expect((await client.getRole('operator')).id).toBe('operator');
    expect((await client.updateRole('operator', {enabled: false})).id).toBe('operator');
    await client.deleteRole('operator');
    expect((await client.listTeams()).teams[0]?.id).toBe('support');
    expect((await client.createTeam({id: 'support', name: 'Support'})).id).toBe('support');
    expect((await client.getTeam('support')).id).toBe('support');
    expect((await client.updateTeam('support', {enabled: false})).id).toBe('support');
    await client.deleteTeam('support');
    expect((await client.replaceUserRBAC('user-1', {roleIds: ['operator'], teamIds: ['support']})).userId).toBe('user-1');
    await client.revokeUserSession('session-1');
    expect((await client.listAdminControlEvents()).items).toEqual([]);
    expect((await client.getAdminControlEvent('event-1')).eventId).toBe('event-1');
    expect((await client.cancelControlEvent('event-1')).eventId).toBe('event-1');
    expect((await client.listLicenses()).items[0]?.licenseId).toBe('lic-1');
    expect((await client.getLicense('lic-1')).licenseId).toBe('lic-1');
    expect((await client.importLicense({license: {format: 'zhiyuan-license-v1', keyId: 'k1', payload: {}, signature: 'sig'}})).licenseId).toBe('lic-1');
    expect((await client.revokeLicense('lic-1')).status).toBe('revoked');
    const paths = server.requests.map(request => `${request.method} ${request.path}`);
    expect(paths).toEqual(expect.arrayContaining([
      'GET /aep/v1/admin/permissions', 'POST /aep/v1/admin/roles', 'PATCH /aep/v1/admin/roles/operator',
      'DELETE /aep/v1/admin/roles/operator', 'POST /aep/v1/admin/teams', 'PATCH /aep/v1/admin/teams/support',
      'DELETE /aep/v1/admin/teams/support', 'PUT /aep/v1/admin/users/user-1/rbac',
      'POST /aep/v1/admin/sessions/session-1/revoke',
      'GET /aep/v1/admin/control-events', 'GET /aep/v1/admin/control-events/event-1',
      'POST /aep/v1/admin/control-events/event-1/cancel', 'POST /aep/v1/admin/licenses/import',
      'POST /aep/v1/admin/licenses/lic-1/revoke',
    ]));
  });

  test('encodes pagination filters for admin resource lists', async () => {
    await client.loginWithPassword({deploymentId: 'ent-1', username: 'demo', password: 'password'});
    await client.listRoles({cursor: 'role-2', limit: 25});
    await client.listTeams({cursor: 'team-2', limit: 25});
    await client.listSkills({cursor: 'skill-2', limit: 25});
    await client.listCredentials({cursor: 'credential-2', limit: 25});
    await client.listAdminModels({cursor: 'model-2', limit: 25});

    const requests = server.requests.filter(request => request.search !== '');
    expect(requests.slice(-5).map(request => request.search)).toEqual([
      '?cursor=role-2&limit=25',
      '?cursor=team-2&limit=25',
      '?cursor=skill-2&limit=25',
      '?cursor=credential-2&limit=25',
      '?cursor=model-2&limit=25',
    ]);
  });
});

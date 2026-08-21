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
      agentId: 'agent-1',
      agentVersion: '0.1.0',
      platform: 'windows',
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
    expect(request?.headers['x-aep-agent-id']).toBe('agent-1');
    expect(request?.headers['x-aep-protocol-version']).toBe('1.0');
    expect(request?.headers['x-request-id']).toBeTruthy();
    expect(server.requests.filter(item => item.path === '/aep/v1/metadata')).toHaveLength(2);
  });

  test('stores login tokens and loads current identity', async () => {
    const tokens = await client.loginWithPassword({enterpriseId: 'ent-1', username: 'demo', password: 'password'});
    expect((await store.get())?.accessToken).toBe(tokens.accessToken);
    expect((await client.getCurrentIdentity()).user.id).toBe('user-1');
  });

  test('discovers models and returns an OpenAI-compatible gateway connection', async () => {
    await expect(client.getModelConnection()).rejects.toMatchObject({code: 'NO_SESSION', status: 401});
    await client.loginWithPassword({enterpriseId: 'ent-1', username: 'demo', password: 'password'});

    expect((await client.listAgentModels()).models[0]).toMatchObject({
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
    await client.loginWithPassword({enterpriseId: 'ent-1', username: 'demo', password: 'password'});
    const write = {
      id: 'model-1',
      displayName: 'Enterprise Model',
      sourceType: 'gateway' as const,
      protocol: 'openai-compatible' as const,
      endpoint: '/openai/v1',
      upstreamModel: 'qwen3-32b',
      capabilities: ['text', 'tools', 'streaming'],
      contextWindow: 131072,
      isDefault: true,
      enabled: true,
      credentialId: null,
    };

    expect((await client.listAdminModels()).models).toHaveLength(1);
    await expect(client.createModel(write)).resolves.toMatchObject({id: 'model-1'});
    await expect(client.getModel('model-1')).resolves.toMatchObject({id: 'model-1'});
    await expect(client.updateModel('model-1', {enabled: false})).resolves.toMatchObject({id: 'model-1'});
    await client.deleteModel('model-1');

    expect((await client.listModelAssignments()).assignments).toHaveLength(1);
    const subjectTypes = ['enterprise', 'organization', 'user', 'agent'] as const;
    for (const type of subjectTypes) {
      await expect(client.createModelAssignment({modelId: 'model-1', subject: {type, id: type + '-1'}})).resolves.toMatchObject({id: 'assignment-1'});
    }
    await client.deleteModelAssignment('assignment-1');
  });

  test('single-flights concurrent 401 refreshes', async () => {
    await client.loginWithPassword({enterpriseId: 'ent-1', username: 'demo', password: 'password'});
    server.delayUnauthorizedResponses([0, 0, 0, 0, 50, 50, 50, 50]);
    server.expireAccessToken();
    const identities = await Promise.all(Array.from({length: 8}, () => client.getCurrentIdentity()));
    expect(identities).toHaveLength(8);
    expect(server.refreshCount).toBe(1);
    expect((await store.get())?.refreshToken).toBe('refresh-2');
  });

  test('clears the session when refresh is rejected', async () => {
    await client.loginWithPassword({enterpriseId: 'ent-1', username: 'demo', password: 'password'});
    server.expireAccessToken();
    server.failRefresh();
    await expect(client.getCurrentIdentity()).rejects.toMatchObject({code: 'REFRESH_TOKEN_INVALID'});
    expect(await store.get()).toBeNull();
  });

  test('supports ETag manifests and binary package downloads', async () => {
    await client.loginWithPassword({enterpriseId: 'ent-1', username: 'demo', password: 'password'});
    const first = await client.getSkillManifest();
    expect(first.notModified).toBe(false);
    const second = await client.getSkillManifest('"skills-1"');
    expect(second).toEqual({notModified: true, etag: '"skills-1"'});
    const archive = await client.downloadSkillPackage('review', '1.0.0');
    expect([...archive]).toEqual([0x50, 0x4b, 0x03, 0x04]);
  });

  test('maps RFC 9457 responses to AepProblem', async () => {
    await client.loginWithPassword({enterpriseId: 'ent-1', username: 'demo', password: 'password'});
    const failure = client.getAgent('missing');
    await expect(failure).rejects.toBeInstanceOf(AepProblem);
    await expect(failure).rejects.toMatchObject({status: 404, code: 'RESOURCE_NOT_FOUND', requestId: 'req-mock'});
  });

  test('covers control, telemetry, and administration APIs', async () => {
    await client.loginWithPassword({enterpriseId: 'ent-1', username: 'demo', password: 'password'});
    expect((await client.heartbeat({agentVersion: '0.1.0', platform: 'windows'})).hasPendingControlEvents).toBe(true);
    expect((await client.listControlEvents()).items).toEqual([]);
    await client.acknowledgeControlEvent('delivery-1', '2026-08-20T00:00:00Z');
    await client.reportControlEventResult('delivery-1', {status: 'succeeded', completedAt: '2026-08-20T00:00:01Z'});
    expect(await client.uploadEventBatch([{eventId: 'event-1', type: 'auth.login'}])).toMatchObject({accepted: ['event-1']});
    expect((await client.listAgents()).items).toEqual([]);
  });
});

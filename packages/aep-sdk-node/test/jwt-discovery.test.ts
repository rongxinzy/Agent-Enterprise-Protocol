import {expect, test} from 'vitest';
import {AepClient, FetchTransport, MemoryTokenStore} from '../src/index.js';
import type {JwtVerifierConfiguration} from '../src/index.js';
import {MockAepServer} from './mock-server.js';

test('discovers JWT verification without session credentials or redirecting to the agent-control base', async () => {
  const server = new MockAepServer();
  await server.start();
  try {
    const store = new MemoryTokenStore();
    const client = new AepClient({baseUrl: server.baseUrl, agentControlBaseUrl: 'http://127.0.0.1:1',
      tokenStore: store, transport: new FetchTransport({defaultTimeoutMs: 2000, maxRetries: 0})});
    const configuration: JwtVerifierConfiguration = await client.getJwtVerifierConfiguration();
    expect(configuration).toEqual({issuer: server.baseUrl, jwks_uri: server.baseUrl + '/.well-known/jwks.json',
      id_token_signing_alg_values_supported: ['EdDSA']});
    expect(server.requests).toHaveLength(1);
    expect(server.requests[0]?.path).toBe('/.well-known/openid-configuration');
    expect(server.requests[0]?.headers.authorization).toBeUndefined();
    expect(await store.get()).toBeNull();

    const loggedIn = new AepClient({baseUrl: server.baseUrl, tokenStore: store});
    await loggedIn.loginWithPassword({deploymentId: 'ent-1', username: 'demo', password: 'password'});
    expect((await store.get())?.accessToken).toBeTruthy();
    await client.getJwtVerifierConfiguration();
    expect(server.requests.at(-1)?.headers.authorization).toBeUndefined();
    expect(server.refreshCount).toBe(0);
  } finally {
    await server.stop();
  }
});

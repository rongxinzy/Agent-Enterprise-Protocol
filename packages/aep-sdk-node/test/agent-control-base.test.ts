import {describe, expect, test, vi} from 'vitest';
import {AepClient, MemoryTokenStore, FetchTransport, type AepTransport, type AepResponse} from '../src/index.js';

function recordingTransport(seen: Array<{base: string; path: string}>): AepTransport {
  return {
    async request<T>(_base: string, request: {path: string}): Promise<AepResponse<T>> {
      seen.push({base: _base, path: request.path});
      return {
        status: 401,
        data: {problem: 'unauthenticated'} as T,
        headers: new Headers(),
      };
    },
  };
}

describe('agent control base URL routing', () => {
  test('agent-surface paths use the split base; discovery and admin stay on the API base', async () => {
    const seen: Array<{base: string; path: string}> = [];
    const client = new AepClient({
      baseUrl: 'https://api.example.com',
      agentControlBaseUrl: 'https://agents.example.com',
      tokenStore: new MemoryTokenStore(),
      transport: recordingTransport(seen),
    });

    // The stub transport answers 401 for everything except nothing; every
    // call throws after the routing decision, which is all these tests
    // assert. Record-and-swallow.
    const attempt = async (call: () => Promise<unknown>) => {
      await call().catch(() => undefined);
    };
    await attempt(() => client.getMetadata());
    await attempt(() => client.getJwks());
    await attempt(() => client.heartbeatUser({status: 'online'}));
    await attempt(() => client.listUserControlEvents());
    await attempt(() => client.listAdminModels());

    expect(seen.map((entry) => [entry.base, entry.path])).toEqual([
      ['https://api.example.com', '/aep/v1/metadata'],
      ['https://api.example.com', '/.well-known/jwks.json'],
      ['https://agents.example.com', '/aep/v1/user/heartbeat'],
      ['https://agents.example.com', '/aep/v1/user/control-events?limit=50'],
      ['https://api.example.com', '/aep/v1/admin/models?'],
    ]);
  });

  test('without agentControlBaseUrl everything stays on the single base', async () => {
    const seen: Array<{base: string; path: string}> = [];
    const client = new AepClient({
      baseUrl: 'https://api.example.com',
      tokenStore: new MemoryTokenStore(),
      transport: recordingTransport(seen),
    });

    await client.getMetadata().catch(() => undefined);
    await client.heartbeatUser({status: 'online'}).catch(() => undefined);

    expect(seen.every((entry) => entry.base === 'https://api.example.com')).toBe(true);
  });

  test('login and refresh route to the agent base in split deployments', async () => {
    const seen: Array<{base: string; path: string}> = [];
    const client = new AepClient({
      baseUrl: 'https://api.example.com',
      agentControlBaseUrl: 'https://agents.example.com',
      tokenStore: new MemoryTokenStore(),
      transport: {
        async request<T>(base: string, request: {path: string; body?: unknown}): Promise<AepResponse<T>> {
          seen.push({base, path: request.path});
          if (request.path === '/aep/v1/auth/password/login') {
            return {
              status: 200,
              data: {accessToken: 'a', refreshToken: 'r', tokenType: 'Bearer', expiresIn: 900} as T,
              headers: new Headers(),
            };
          }
          return {status: 401, data: {} as T, headers: new Headers()};
        },
      },
    });

    await client.loginWithPassword({deploymentId: 'demo', username: 'u', password: 'p'});
    expect(seen).toEqual([{base: 'https://agents.example.com', path: '/aep/v1/auth/password/login'}]);
  });
});

import {describe, expect, test, vi} from 'vitest';
import {
  AepClient, AepProblem, FetchTransport, MemoryTokenStore,
  type AepTokens, type DataPlanePublication, type DataPlanePublicationCondition,
} from '../src/index.js';

const path = '/aep/v1/admin/data-plane/publications';
const operationId = '81e147fc-c3b9-49b0-8ecf-0a88386734dc';
const createdAt = '2026-10-15T00:00:00Z';
const tokens: AepTokens = {
  accessToken: 'access-fixture', refreshToken: 'refresh-fixture', modelAccessToken: 'model-fixture',
  tokenType: 'Bearer', expiresIn: 3600, modelAccessExpiresIn: 3600, passwordChangeRequired: false,
};

function publication(overrides: Partial<DataPlanePublication> = {}): DataPlanePublication {
  const types: DataPlanePublicationCondition['type'][] = ['Accepted', 'Applied', 'Ready'];
  return {
    operationId, deploymentId: 'deployment-fixture', revision: 'catalog-fixture', contentHash: 'a'.repeat(64),
    phase: 'queued', createdAt, updatedAt: createdAt, idempotencyExpiresAt: '2026-10-16T00:00:00Z',
    conditions: types.map(type => ({type, status: 'Unknown', observedRevision: null, reason: 'NotObserved', lastTransitionTime: createdAt})),
    errorCode: null, ...overrides,
  };
}

function json(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), {status, headers: {
    'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json',
    'Cache-Control': 'no-store',
    'X-Request-ID': 'request-fixture',
    ...(status === 202 ? {Location: `${path}/${operationId}`} : {}),
  }});
}

function problem(status: number, code: string): Response {
  return json({type: 'about:blank', title: 'Publication problem', status, code}, status);
}

async function fixture(handler: (url: URL, init: RequestInit) => Response | Promise<Response>) {
  const fetch = vi.fn<typeof globalThis.fetch>(async (input, init) => handler(new URL(String(input)), init ?? {}));
  const store = new MemoryTokenStore();
  await store.set({...tokens});
  const client = new AepClient({
    baseUrl: 'https://api.example.test', agentControlBaseUrl: 'https://agents.example.test', tokenStore: store,
    transport: new FetchTransport({fetch, maxRetries: 2}),
  });
  return {client, fetch};
}

describe('asynchronous data-plane publication SDK contract (mock service)', () => {
  test('publishes limits explicitly and keeps retries bound to the original combined intent', async () => {
    const seen: string[] = [];
    const input = {includeGatewayLimits: true, revision: 'combined-1'};
    const {client} = await fixture((_url, init) => {
      seen.push(String(init.body));
      if (seen.length === 1) {
        input.includeGatewayLimits = false;
        throw new TypeError('Mock acknowledgement lost');
      }
      return json(publication({revision: 'combined-1'}), 202);
    });
    await client.createDataPlanePublication('combined-fixture', input);
    expect(seen).toEqual([
      '{"includeGatewayLimits":true,"revision":"combined-1"}',
      '{"includeGatewayLimits":true,"revision":"combined-1"}',
    ]);
  });

  test('sends a management request with a caller key, and queries its operation without inferring Ready', async () => {
    const queued = publication();
    const projected = publication({phase: 'projected', conditions: queued.conditions.map(condition => (
      condition.type === 'Ready' ? {...condition, reason: 'ReadinessNotImplemented'}
        : {...condition, status: 'True', observedRevision: queued.revision, reason: condition.type}
    ))});
    const {client, fetch} = await fixture((url, init) => {
      expect(url.origin).toBe('https://api.example.test');
      expect(url.search).toBe('');
      const headers = new Headers(init.headers);
      expect(headers.get('X-AEP-Protocol-Version')).toBe('1.0');
      expect(headers.get('Authorization')).toBe('Bearer access-fixture');
      expect(headers.get('X-Request-ID')).toBeTruthy();
      if (init.method === 'POST') {
        expect(url.pathname).toBe(path);
        expect(headers.get('Idempotency-Key')).toBe('publication:fixture-1');
        expect(headers.get('Content-Type')).toBe('application/json');
        expect(JSON.parse(String(init.body))).toEqual({});
        return json(queued, 202);
      }
      expect(init.method).toBe('GET');
      expect(url.pathname).toBe(`${path}/${operationId}`);
      expect(headers.has('Idempotency-Key')).toBe(false);
      return json(projected);
    });
    expect(await client.createDataPlanePublication('publication:fixture-1')).toEqual(queued);
    // A 202 must not trigger automatic polling.
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(await client.getDataPlanePublication(operationId)).toEqual(projected);
    expect(projected.conditions.find(condition => condition.type === 'Ready')?.status).toBe('Unknown');
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  test('retries a lost acknowledgement using the same key and frozen request body', async () => {
    const input = {revision: 'release-1'};
    const result = publication({revision: input.revision});
    const seen: Array<{key: string | null; body: string}> = [];
    const {client, fetch} = await fixture((url, init) => {
      expect(url.pathname).toBe(path);
      seen.push({key: new Headers(init.headers).get('Idempotency-Key'), body: String(init.body)});
      if (seen.length === 1) {
        input.revision = 'caller-mutated';
        // Simulate a committed operation whose response was lost, not a real outbox test.
        throw new TypeError('Mock connection lost after commit');
      }
      return json(result, 202);
    });
    expect(await client.createDataPlanePublication('retry-fixture', input)).toEqual(result);
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(seen).toEqual([
      {key: 'retry-fixture', body: '{"revision":"release-1"}'},
      {key: 'retry-fixture', body: '{"revision":"release-1"}'},
    ]);
  });

  test('preserves the operation identity and terminal status on an explicit replay', async () => {
    let calls = 0;
    const original = publication();
    const superseded = publication({phase: 'superseded'});
    const {client} = await fixture((_url, init) => {
      expect(new Headers(init.headers).get('Idempotency-Key')).toBe('replay-fixture');
      expect(init.body).toBe('{}');
      return json(++calls === 1 ? original : superseded, 202);
    });
    const first = await client.createDataPlanePublication('replay-fixture');
    const replay = await client.createDataPlanePublication('replay-fixture');
    expect(replay.operationId).toBe(first.operationId);
    expect(replay.revision).toBe(first.revision);
    expect(replay.contentHash).toBe(first.contentHash);
    expect(replay.idempotencyExpiresAt).toBe(first.idempotencyExpiresAt);
    expect(replay.phase).toBe('superseded');
  });

  test('retains key and body when an expired access token is refreshed', async () => {
    const result = publication();
    const postRequests: RequestInit[] = [];
    const {client} = await fixture((url, init) => {
      if (url.pathname === '/aep/v1/auth/refresh') return json({...tokens, accessToken: 'access-refreshed'});
      expect(url.pathname).toBe(path);
      postRequests.push(init);
      return postRequests.length === 1 ? problem(401, 'TOKEN_INVALID') : json(result, 202);
    });
    expect(await client.createDataPlanePublication('refresh-fixture', {revision: 'release-1'})).toEqual(result);
    expect(postRequests).toHaveLength(2);
    for (const request of postRequests) {
      expect(new Headers(request.headers).get('Idempotency-Key')).toBe('refresh-fixture');
      expect(request.body).toBe('{"revision":"release-1"}');
    }
    expect(new Headers(postRequests[1]?.headers).get('Authorization')).toBe('Bearer access-refreshed');
  });

  test.each([
    [400, 'INVALID_IDEMPOTENCY_KEY'], [403, 'PERMISSION_DENIED'],
    [409, 'IDEMPOTENCY_KEY_CONFLICT'], [422, 'INVALID_DATA_PLANE_STATE'],
    [404, 'RESOURCE_NOT_FOUND'],
  ])('maps %i %s without retries or synchronous fallback', async (status, code) => {
    const {client, fetch} = await fixture(() => problem(status, code));
    const failure = client.createDataPlanePublication('error-fixture');
    await expect(failure).rejects.toBeInstanceOf(AepProblem);
    await expect(failure).rejects.toMatchObject({status, code, requestId: 'request-fixture'});
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(new URL(String(fetch.mock.calls[0]?.[0])).pathname).toBe(path);
  });

  test('bounds transient retries and leaves recovery to the caller with the same key', async () => {
    const {client, fetch} = await fixture(() => problem(503, 'TEMPORARILY_UNAVAILABLE'));
    await expect(client.createDataPlanePublication('unavailable-fixture')).rejects.toMatchObject({status: 503});
    expect(fetch).toHaveBeenCalledTimes(3);
    for (const [url, init] of fetch.mock.calls) {
      expect(new URL(String(url)).pathname).toBe(path);
      expect(new Headers(init?.headers).get('Idempotency-Key')).toBe('unavailable-fixture');
    }
  });

  test.each(['', 'contains space', 'contains\nnewline', 'trailing-newline\n', '汉字', 'x'.repeat(129), undefined, null, 42])('rejects unsafe key %j before sending', async key => {
    const {client, fetch} = await fixture(() => json(publication(), 202));
    // Exercise JavaScript callers that do not have TypeScript's string constraint.
    expect(() => client.createDataPlanePublication(key as string)).toThrow(TypeError);
    expect(fetch).not.toHaveBeenCalled();
  });

  test('encodes the operation ID as one path segment and surfaces not-found', async () => {
    const {client, fetch} = await fixture((url, init) => {
      expect(init.method).toBe('GET');
      expect(url.pathname).toBe(`${path}/other%2Foperation%3Fsecret%3Dignored`);
      expect(url.search).toBe('');
      return problem(404, 'RESOURCE_NOT_FOUND');
    });
    await expect(client.getDataPlanePublication('other/operation?secret=ignored')).rejects.toMatchObject({
      status: 404, code: 'RESOURCE_NOT_FOUND',
    });
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  test('does not replace Unknown or failed observations with a fabricated ready result', async () => {
    const result = publication({phase: 'projected', conditions: publication().conditions.map(condition => (
      condition.type === 'Accepted' ? {...condition, status: 'False', observedRevision: 'catalog-fixture', reason: 'InvalidSpec'} : condition
    ))});
    const {client} = await fixture(() => json(result));
    expect(await client.getDataPlanePublication(operationId)).toEqual(result);
  });
});

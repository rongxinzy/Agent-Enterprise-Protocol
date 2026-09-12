import {describe, expect, test, vi} from 'vitest';

import {FetchTransport, HttpMethod} from '../src/index.js';

describe('FetchTransport redirect policy', () => {
  test.each([
    {method: HttpMethod.Get, path: '/aep/v1/metadata'},
    {method: HttpMethod.Post, path: '/aep/v1/auth/refresh'},
  ])('rejects automatic redirects for $method requests', async request => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
      new Response(null, {status: 204}),
    );
    const transport = new FetchTransport({fetch, maxRetries: 0});

    await transport.request('https://control.example', {
      ...request,
      headers: {Authorization: 'Bearer sensitive-token'},
      body: request.method === HttpMethod.Post ? {refreshToken: 'sensitive-refresh-token'} : undefined,
    });

    expect(fetch).toHaveBeenCalledOnce();
    expect(fetch.mock.calls[0]?.[1]).toMatchObject({
      method: request.method,
      redirect: 'error',
    });
  });
});

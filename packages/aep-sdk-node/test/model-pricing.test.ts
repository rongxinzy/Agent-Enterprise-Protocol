import {describe, expect, test} from 'vitest';
import {AepClient, MemoryTokenStore, type AepRequest, type AepTransport} from '../src/index.js';

describe('model price configuration contract', () => {
  test('preserves exact decimal prices, API origin, optimistic versions and clear semantics', async () => {
    const requests: AepRequest[] = [];
    const result = {modelId: 'model/one', version: 2, updatedAt: '2026-10-10T00:00:00Z',
      pricing: {currency: 'CNY', inputPricePerMillionTokens: '0.000001', outputPricePerMillionTokens: '0',
        cachedInputPricePerMillionTokens: '0.01', source: 'Operator reference'}};
    const transport: AepTransport = {async request<T>(baseUrl, request) {
      expect(baseUrl).toBe('https://api.example.test');
      requests.push(request);
      return {status: 200, headers: new Headers(), data: result as T};
    }};
    const client = new AepClient({baseUrl: 'https://api.example.test',
      agentControlBaseUrl: 'https://agent.example.test', tokenStore: new MemoryTokenStore(), transport});
    expect(await client.getModelPricing('model/one')).toEqual(result);
    expect(requests[0]?.path).toBe('/aep/v1/admin/models/model%2Fone/pricing');
    await client.putModelPricing('model/one', {pricing: result.pricing, expectedVersion: 1});
    expect(requests.at(-1)).toMatchObject({method: 'PUT', body: {pricing: result.pricing, expectedVersion: 1}, retry: false});
    await client.putModelPricing('model/one', {pricing: null, expectedVersion: 2});
    expect(requests.at(-1)).toMatchObject({body: {pricing: null, expectedVersion: 2}, retry: false});
  });
});

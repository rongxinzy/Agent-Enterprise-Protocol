import {describe, expect, test} from 'vitest';
import {AepClient, MemoryTokenStore, type AepRequest, type AepTransport} from '../src/index.js';

describe('native gateway SDK contracts', () => {
  test('keeps management requests on the API origin and preserves native results', async () => {
    const requests: AepRequest[] = [];
    const bases: string[] = [];
    const native = {source: 'prometheus', queriedAt: '2026-10-09T00:00:00Z',
      definition: {id: 'ai_input_tokens', unit: 'tokens', aggregation: 'counter_increase',
        windowSeconds: 60, groupBy: 'none', modelDimension: 'not_applicable'},
      data: {resultType: 'matrix', result: [{values: [[1, '7.25']]}]}};
    const transport: AepTransport = {
      async request<T>(baseUrl: string, request: AepRequest) {
        bases.push(baseUrl);
        requests.push(request);
        return {status: 200, headers: new Headers(), data: native as T};
      },
    };
    const client = new AepClient({baseUrl: 'https://api.example.test', agentControlBaseUrl: 'https://agent.example.test',
      tokenStore: new MemoryTokenStore(), transport});
    expect(await client.queryGatewayMetrics({metric: 'input_tokens', start: '2026-10-09T00:00:00Z',
      end: '2026-10-09T01:00:00Z', userId: 'user & one', expectedDefinition: 'ai_input_tokens'})).toEqual(native);
    expect(bases[0]).toBe('https://api.example.test');
    expect(requests[0]?.path).toContain('/aep/v1/admin/model-gateway/metrics?');
    expect(requests[0]?.path).toContain('userId=user+%26+one');
    expect(requests[0]?.path).toContain('expectedDefinition=ai_input_tokens');
    await client.changeGatewayQuota('user/one', 100);
    expect(requests.at(-1)?.retry).toBe(false);
    expect(requests.at(-1)?.path).toContain('user%2Fone/delta');
    await client.putGatewayLimit('rule/one', {kind: 'requests', scopeType: 'global', maximum: 60,
      interval: 'minute', enabled: true, expectedVersion: 0});
    expect(requests.at(-1)?.retry).toBe(false);
    await client.createGatewayTestAccess('model/one');
    expect(requests.at(-1)?.retry).toBe(false);
  });
});

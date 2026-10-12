import {describe, expect, test} from 'vitest';
import {AepClient, AepProblem, MemoryTokenStore, type AepRequest, type AepTransport} from '../src/index.js';

function fixtureTransport(handlers: {
  readonly [key: string]: (request: AepRequest) => {status: number; data: unknown};
}) {
  const requests: AepRequest[] = [];
  const transport: AepTransport = {async request<T>(baseUrl, request) {
    expect(baseUrl).toBe('https://api.example.test');
    requests.push(request);
    const handler = handlers[`${request.method} ${request.path}`];
    if (!handler) throw new Error(`Unexpected request ${request.method} ${request.path}`);
    const result = handler(request);
    return {status: result.status, headers: new Headers(), data: result.data as T};
  }};
  const client = new AepClient({baseUrl: 'https://api.example.test', tokenStore: new MemoryTokenStore(), transport});
  return {client, requests};
}

function problem(status: number, code: string) {
  return {status, data: {type: `https://aep.example/problems/${code.toLowerCase()}`, title: 'Problem', status, code, detail: 'Fixture problem.', requestId: 'req-fixture'}};
}

describe('compute governance contract', () => {
  test('reads the compute tier catalog with protocol headers', async () => {
    const catalog = {items: [
      {name: 'standard', description: 'Default tier', cpuRequest: '250m', cpuLimit: '1000m', memoryRequest: '512Mi', memoryLimit: '1Gi', source: 'deployment'},
    ]};
    const {client, requests} = fixtureTransport({'GET /aep/v1/admin/compute/tiers': () => ({status: 200, data: catalog})});
    expect(await client.getComputeTierCatalog()).toEqual(catalog);
    expect(requests[0]?.method).toBe('GET');
    expect(requests[0]?.headers?.['X-AEP-Protocol-Version']).toBe('1.0');
  });

  test('returns an empty tier catalog when unconfigured', async () => {
    const {client} = fixtureTransport({'GET /aep/v1/admin/compute/tiers': () => ({status: 200, data: {items: []}})});
    expect(await client.getComputeTierCatalog()).toEqual({items: []});
  });

  test('lists per-agent compute assignments', async () => {
    const assignments = {items: [
      {agentId: 'agent-1', agentName: 'Reviewer', tier: {override: 'performance', effective: 'performance', source: 'override'}, updatedAt: '2026-10-11T00:00:00Z', updatedBy: 'operator-1'},
      {agentId: 'agent-2', tier: {override: null, effective: 'standard', source: 'default'}},
    ]};
    const {client, requests} = fixtureTransport({'GET /aep/v1/admin/compute/assignments': () => ({status: 200, data: assignments})});
    expect(await client.listComputeAssignments()).toEqual(assignments);
    expect(requests[0]?.method).toBe('GET');
    expect(requests[0]?.headers?.['X-AEP-Protocol-Version']).toBe('1.0');
  });

  test('stores a tier override with an encoded agent path segment', async () => {
    const updated = {agentId: 'agent/one', tier: {override: 'performance', effective: 'performance', source: 'override'}};
    const {client, requests} = fixtureTransport({
      'PUT /aep/v1/admin/compute/assignments/agent%2Fone': () => ({status: 200, data: updated}),
    });
    expect(await client.updateComputeAssignment('agent/one', 'performance')).toEqual(updated);
    expect(requests[0]?.method).toBe('PUT');
    expect(requests[0]?.body).toEqual({tier: 'performance'});
    expect(requests[0]?.headers?.['X-AEP-Protocol-Version']).toBe('1.0');
  });

  test('sends an explicit null tier to clear the override', async () => {
    const cleared = {agentId: 'agent-1', tier: {override: null, effective: 'standard', source: 'default'}};
    const {client, requests} = fixtureTransport({
      'PUT /aep/v1/admin/compute/assignments/agent-1': () => ({status: 200, data: cleared}),
    });
    expect(await client.updateComputeAssignment('agent-1', null)).toEqual(cleared);
    const body = requests[0]?.body;
    expect(body).toEqual({tier: null});
    expect(body !== null && typeof body === 'object' && 'tier' in body).toBe(true);
  });

  test('maps an unknown agent to a 404 AepProblem', async () => {
    const {client} = fixtureTransport({
      'PUT /aep/v1/admin/compute/assignments/missing': () => problem(404, 'RESOURCE_NOT_FOUND'),
    });
    const failure = client.updateComputeAssignment('missing', 'standard');
    await expect(failure).rejects.toBeInstanceOf(AepProblem);
    await expect(failure).rejects.toMatchObject({status: 404, code: 'RESOURCE_NOT_FOUND', requestId: 'req-fixture'});
  });

  test('maps an unknown tier to a 422 AepProblem', async () => {
    const {client} = fixtureTransport({
      'PUT /aep/v1/admin/compute/assignments/agent-1': () => problem(422, 'VALIDATION_FAILED'),
    });
    const failure = client.updateComputeAssignment('agent-1', 'platinum');
    await expect(failure).rejects.toBeInstanceOf(AepProblem);
    await expect(failure).rejects.toMatchObject({status: 422, code: 'VALIDATION_FAILED', requestId: 'req-fixture'});
  });

  test('keeps compute integer setting values as numbers in both directions', async () => {
    const settings = {
      modelGatewayBaseUrl: {override: null, effectiveValue: 'https://gateway.example.test/v1', source: 'env'},
      modelFallbackIds: {override: null, effectiveValue: [], source: 'unset'},
      computeDefaultTier: {override: null, effectiveValue: 'standard', source: 'env'},
      computeIdleHibernateSeconds: {override: 300, effectiveValue: 300, source: 'override'},
      computeConcurrencyCap: {override: null, effectiveValue: 25, source: 'env'},
    };
    const {client, requests} = fixtureTransport({
      'GET /aep/v1/admin/deployment/settings': () => ({status: 200, data: settings}),
      'PUT /aep/v1/admin/deployment/settings': () => ({status: 200, data: settings}),
    });
    const read = await client.getDeploymentSettings();
    expect(typeof read.computeIdleHibernateSeconds.override).toBe('number');
    expect(typeof read.computeConcurrencyCap.effectiveValue).toBe('number');
    await client.updateDeploymentSettings({computeIdleHibernateSeconds: 900, computeConcurrencyCap: 10});
    const body = requests.at(-1)?.body;
    expect(body).toMatchObject({computeIdleHibernateSeconds: 900, computeConcurrencyCap: 10});
    expect(typeof (body as {computeIdleHibernateSeconds: unknown}).computeIdleHibernateSeconds).toBe('number');
  });
});

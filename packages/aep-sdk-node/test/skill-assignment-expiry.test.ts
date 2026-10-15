import {describe, expect, test} from 'vitest';
import {AepClient, AepProblem, MemoryTokenStore, type AepRequest, type AepTransport} from '../src/index.js';

// The public client is the contract surface for integrators: the generated
// types alone do not let anyone call the new operation.
describe('Skill assignment expiry contract', () => {
  const accepted = {id: 'assignment-one', skillId: 'writer', subject: {type: 'user', id: 'user-a'}, expiresAt: null};

  function client(respond: (request: AepRequest) => {status: number; data?: unknown}, requests: AepRequest[] = []) {
    const transport: AepTransport = {
      async request<T>(baseUrl: string, request: AepRequest) {
        expect(baseUrl).toBe('https://api.example.test');
        requests.push(request);
        const answer = respond(request);
        return {status: answer.status, headers: new Headers(), data: answer.data as T};
      },
    };
    return new AepClient({
      baseUrl: 'https://api.example.test',
      tokenStore: new MemoryTokenStore(),
      transport,
    });
  }

  test('patches the expiry in place, and clears it with null', async () => {
    const requests: AepRequest[] = [];
    // The stub echoes the patched field, so the response reflects the request.
    const subject = client(request => ({status: 200, data: {...accepted, ...(request.body as object)}}), requests);

    expect(await subject.updateSkillAssignment('assignment/one', {expiresAt: '2026-12-31T00:00:00Z'})).toMatchObject({
      expiresAt: '2026-12-31T00:00:00Z',
    });
    expect(requests.at(-1)).toMatchObject({
      method: 'PATCH',
      path: '/aep/v1/admin/skill-assignments/assignment%2Fone',
      body: {expiresAt: '2026-12-31T00:00:00Z'},
    });

    expect(await subject.updateSkillAssignment('assignment-one', {expiresAt: null})).toMatchObject({expiresAt: null});
    expect(requests.at(-1)).toMatchObject({method: 'PATCH', body: {expiresAt: null}});
  });

  test('surfaces the problem code for refused updates', async () => {
    const requests: AepRequest[] = [];
    const missing = client(
      () => ({status: 404, data: {type: 'about:blank', title: 'Not found', code: 'RESOURCE_NOT_FOUND'}}),
      requests,
    );
    await expect(missing.updateSkillAssignment('missing', {expiresAt: null})).rejects.toBeInstanceOf(AepProblem);
    await expect(missing.updateSkillAssignment('missing', {expiresAt: null})).rejects.toMatchObject({
      status: 404,
      code: 'RESOURCE_NOT_FOUND',
    });

    const invalid = client(() => ({status: 400, data: {type: 'about:blank', title: 'Bad request', code: 'INVALID_REQUEST'}}));
    await expect(
      invalid.updateSkillAssignment('assignment-one', {expiresAt: 'not-a-timestamp'}),
    ).rejects.toMatchObject({status: 400, code: 'INVALID_REQUEST'});
  });
});

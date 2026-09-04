import http from 'node:http';

import type {CredentialMetadata, ResolvedCredential} from '@aep/sdk-node';
import {afterEach, expect, test} from 'vitest';

import {CredentialHttpClient, CredentialManager, CredentialUnavailableError} from '../src/credentials.js';
import {AgentState} from '../src/state.js';

const servers: http.Server[] = [];

afterEach(async () => {
  await Promise.all(servers.splice(0).map(server => new Promise<void>(resolve => server.close(() => resolve()))));
});

test('bounds resolved material lifetime and respects server expiry', async () => {
  let now = 1_000;
  const client = fakeClient([metadata('revision-1')], [
    material('first', new Date(now + 5_000).toISOString()),
    material('second', null),
  ]);
  const manager = new CredentialManager(client, {maxCacheMs: 10_000, now: () => now});

  await expect(useValue(manager)).resolves.toBe('first');
  now += 4_999;
  await expect(useValue(manager)).resolves.toBe('first');
  now += 2;
  await expect(useValue(manager)).resolves.toBe('second');
  expect(client.resolveCalls).toBe(2);
});

test('metadata rotation invalidates cached material and concurrent resolution is single flight', async () => {
  const client = fakeClient([metadata('revision-1')], [material('old', null), material('new', null)]);
  const manager = new CredentialManager(client);
  await expect(Promise.all([useValue(manager), useValue(manager)])).resolves.toEqual(['old', 'old']);
  expect(client.resolveCalls).toBe(1);

  client.credentials = [metadata('revision-2')];
  await expect(useValue(manager)).resolves.toBe('new');
  expect(client.resolveCalls).toBe(2);
});

test('does not share an in-flight resolution across metadata revisions', async () => {
  let credentials = [metadata('revision-1')];
  let resolveOld: ((value: ResolvedCredential) => void) | undefined;
  let resolveCalls = 0;
  const client = {
    async listCredentialsForUser() {
      return {credentials};
    },
    resolveCredentialForUser() {
      resolveCalls += 1;
      if (resolveCalls === 1) {
        return new Promise<ResolvedCredential>(resolve => {
          resolveOld = resolve;
        });
      }
      return Promise.resolve(material('new', null));
    },
  };
  const manager = new CredentialManager(client);

  const oldUse = useValue(manager);
  await expect.poll(() => resolveCalls).toBe(1);
  credentials = [metadata('revision-2')];
  await expect(useValue(manager)).resolves.toBe('new');
  resolveOld?.(material('old', null));
  await expect(oldUse).resolves.toBe('old');
  expect(resolveCalls).toBe(2);
});

test('assignment revocation removes cached material and prevents resolution', async () => {
  const client = fakeClient([metadata('revision-1')], [material('secret', null)]);
  const manager = new CredentialManager(client);
  await expect(useValue(manager)).resolves.toBe('secret');

  client.credentials = [];
  await expect(useValue(manager)).rejects.toBeInstanceOf(CredentialUnavailableError);
  expect(client.resolveCalls).toBe(1);
});

test('uses a Credential without persisting material in telemetry', async () => {
  const secret = 'must-never-be-persisted';
  const authorizations: Array<string | undefined> = [];
  const url = await startServer((request, response) => {
    authorizations.push(request.headers.authorization);
    response.writeHead(204).end();
  });
  const state = new AgentState(':memory:');
  const client = fakeClient([metadata('revision-1')], [material(secret, null)]);
  try {
    const result = await new CredentialHttpClient(new CredentialManager(client), state).get(
      'credential-1',
      url,
      'unit test',
    );
    expect(result).toEqual({credentialId: 'credential-1', service: 'example-service', status: 204});
    expect(authorizations).toEqual([`Bearer ${secret}`]);
    expect(JSON.stringify(state.listTelemetry())).not.toContain(secret);
  } finally {
    state.close();
  }
});

function metadata(updatedAt: string): CredentialMetadata {
  return {
    id: 'credential-1',
    name: 'Example',
    service: 'example-service',
    type: 'api_key',
    deliveryMode: 'client',
    maskedValue: '***cret',
    enabled: true,
    updatedAt,
  };
}

function material(value: string, expiresAt: string | null): ResolvedCredential {
  return {credentialId: 'credential-1', type: 'api_key', value, expiresAt};
}

function fakeClient(initial: CredentialMetadata[], materials: ResolvedCredential[]) {
  return {
    credentials: initial,
    resolveCalls: 0,
    async listCredentialsForUser() {
      return {credentials: this.credentials};
    },
    async resolveCredentialForUser() {
      const value = materials[this.resolveCalls++];
      if (!value) throw new Error('No fake Credential material remains.');
      await Promise.resolve();
      return value;
    },
  };
}

function useValue(manager: CredentialManager): Promise<string> {
  return manager.use('credential-1', 'test', async value => value.value).then(result => result.result);
}

async function startServer(handler: (request: http.IncomingMessage, response: http.ServerResponse) => void): Promise<string> {
  const server = http.createServer(handler);
  servers.push(server);
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  const address = server.address();
  if (!address || typeof address === 'string') throw new Error('Credential consumer did not bind a TCP port.');
  return `http://127.0.0.1:${address.port}`;
}

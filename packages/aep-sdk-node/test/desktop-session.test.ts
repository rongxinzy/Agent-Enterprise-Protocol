import {afterEach, beforeEach, describe, expect, test} from 'vitest';

import {
  AepClient,
  FetchTransport,
  ProtectedRefreshTokenStore,
  type AepProtectedStorage,
  type AepTokens,
} from '../src/index.js';
import {MockAepServer} from './mock-server.js';

describe('desktop protected session', () => {
  let server: MockAepServer;
  let storage: InspectableProtectedStorage;

  beforeEach(async () => {
    server = new MockAepServer();
    await server.start();
    storage = new InspectableProtectedStorage();
  });

  afterEach(async () => {
    await server.stop();
  });

  test('persists only the refresh token and keeps short-lived tokens in memory', async () => {
    const store = new ProtectedRefreshTokenStore(storage);
    await store.set(tokens('access-secret', 'refresh-secret', 'model-secret'));

    expect((await store.get())?.accessToken).toBe('access-secret');
    expect(storage.lastPlaintext).toBe('refresh-secret');
    expect(storage.writes).toEqual(['refresh-secret']);

    const restarted = new ProtectedRefreshTokenStore(storage);
    expect(await restarted.get()).toBeNull();
    expect(await restarted.getRefreshToken()).toBe('refresh-secret');
  });

  test('single-flights cold-start restoration and rotates protected refresh state', async () => {
    const initial = new ProtectedRefreshTokenStore(storage);
    await initial.set(tokens('discarded-access', 'refresh-1', 'discarded-model'));
    const restarted = new ProtectedRefreshTokenStore(storage);
    const client = createClient(restarted);

    await expect(client.getSessionState()).resolves.toEqual({status: 'recoverable'});
    const [restored, identity] = await Promise.all([client.restoreSession(), client.getCurrentIdentity()]);
    expect(restored?.refreshToken).toBe('refresh-2');
    expect(identity.user.id).toBe('user-1');
    expect(server.refreshCount).toBe(1);
    await expect(client.getSessionState()).resolves.toEqual({status: 'authenticated', passwordChangeRequired: false});
    expect(await restarted.getRefreshToken()).toBe('refresh-2');
  });

  test('clears protected state when cold-start refresh is rejected', async () => {
    const initial = new ProtectedRefreshTokenStore(storage);
    await initial.set(tokens('discarded-access', 'refresh-1', 'discarded-model'));
    const restarted = new ProtectedRefreshTokenStore(storage);
    server.failRefresh();

    await expect(createClient(restarted).restoreSession()).rejects.toMatchObject({code: 'REFRESH_TOKEN_INVALID'});
    expect(await restarted.getRefreshToken()).toBeNull();
    expect(storage.size).toBe(0);
  });

  test('restores before logout and removes protected state', async () => {
    const initial = new ProtectedRefreshTokenStore(storage);
    await initial.set(tokens('discarded-access', 'refresh-1', 'discarded-model'));
    const restarted = new ProtectedRefreshTokenStore(storage);

    await createClient(restarted).logout();

    expect(server.refreshCount).toBe(1);
    expect(storage.size).toBe(0);
    await expect(createClient(restarted).getSessionState()).resolves.toEqual({status: 'signed-out'});
  });

  test('does not retain tokens in memory when protected persistence fails', async () => {
    storage.failWrites = true;
    const store = new ProtectedRefreshTokenStore(storage);
    await expect(store.set(tokens('access-secret', 'refresh-secret', 'model-secret'))).rejects.toThrow('storage unavailable');
    expect(await store.get()).toBeNull();
  });

  test('clears stale state when rotation persistence or decoding fails', async () => {
    const initial = new ProtectedRefreshTokenStore(storage);
    await initial.set(tokens('discarded-access', 'refresh-1', 'discarded-model'));
    const restarted = new ProtectedRefreshTokenStore(storage);
    storage.failWrites = true;

    await expect(createClient(restarted).restoreSession()).rejects.toThrow('storage unavailable');
    expect(await restarted.get()).toBeNull();
    expect(storage.size).toBe(0);

    storage.failWrites = false;
    storage.setRaw('aep.refresh-token', new Uint8Array([0xff]));
    await expect(restarted.getRefreshToken()).rejects.toThrow('could not be decoded');
    expect(storage.size).toBe(0);
  });

  function createClient(tokenStore: ProtectedRefreshTokenStore): AepClient {
    return new AepClient({
      baseUrl: server.baseUrl,
      tokenStore,
      transport: new FetchTransport({defaultTimeoutMs: 2_000, maxRetries: 0}),
    });
  }
});

class InspectableProtectedStorage implements AepProtectedStorage {
  readonly #values = new Map<string, Uint8Array>();
  lastPlaintext = '';
  readonly writes: string[] = [];
  failWrites = false;

  get size(): number {
    return this.#values.size;
  }

  async read(key: string): Promise<Uint8Array | null> {
    return this.#values.get(key)?.slice() ?? null;
  }

  async write(key: string, value: Uint8Array): Promise<void> {
    if (this.failWrites) throw new Error('storage unavailable');
    this.lastPlaintext = new TextDecoder().decode(value);
    this.writes.push(this.lastPlaintext);
    this.#values.set(key, value.slice());
  }

  async remove(key: string): Promise<void> {
    this.#values.delete(key);
  }

  setRaw(key: string, value: Uint8Array): void {
    this.#values.set(key, value.slice());
  }
}

function tokens(accessToken: string, refreshToken: string, modelAccessToken: string): AepTokens {
  return {
    accessToken,
    refreshToken,
    modelAccessToken,
    tokenType: 'Bearer',
    expiresIn: 300,
    modelAccessExpiresIn: 300,
    passwordChangeRequired: false,
  };
}

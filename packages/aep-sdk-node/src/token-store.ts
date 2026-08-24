import type {AepProtectedStorage, AepTokens, AepTokenStore} from './types.js';

const defaultRefreshTokenKey = 'aep.refresh-token';
const maximumRefreshTokenBytes = 8192;

export class MemoryTokenStore implements AepTokenStore {
  #tokens: AepTokens | null;

  constructor(initialTokens: AepTokens | null = null) {
    this.#tokens = initialTokens;
  }

  async get(): Promise<AepTokens | null> {
    return this.#tokens ? {...this.#tokens} : null;
  }

  async set(tokens: AepTokens): Promise<void> {
    this.#tokens = {...tokens};
  }

  async clear(): Promise<void> {
    this.#tokens = null;
  }
}

export class ProtectedRefreshTokenStore implements AepTokenStore {
  readonly #storage: AepProtectedStorage;
  readonly #key: string;
  #tokens: AepTokens | null = null;

  constructor(storage: AepProtectedStorage, key = defaultRefreshTokenKey) {
    if (!key) throw new Error('Protected refresh-token storage key is required.');
    this.#storage = storage;
    this.#key = key;
  }

  async get(): Promise<AepTokens | null> {
    return this.#tokens ? {...this.#tokens} : null;
  }

  async set(tokens: AepTokens): Promise<void> {
    const encoded = new TextEncoder().encode(tokens.refreshToken);
    try {
      await this.#storage.write(this.#key, encoded);
    } catch (error) {
      this.#tokens = null;
      try {
        await this.#storage.remove(this.#key);
      } catch {
        // Preserve the write failure; stale state will be rejected on recovery.
      }
      throw error;
    } finally {
      encoded.fill(0);
    }
    this.#tokens = {...tokens};
  }

  async getRefreshToken(): Promise<string | null> {
    if (this.#tokens) return this.#tokens.refreshToken;
    const encoded = await this.#storage.read(this.#key);
    if (!encoded) return null;
    try {
      if (encoded.byteLength === 0 || encoded.byteLength > maximumRefreshTokenBytes) {
        throw new Error('Protected refresh token is empty or too large.');
      }
      const refreshToken = new TextDecoder('utf-8', {fatal: true}).decode(encoded);
      if (!refreshToken) throw new Error('Protected refresh token is empty.');
      return refreshToken;
    } catch (error) {
      try {
        await this.#storage.remove(this.#key);
      } catch {
        // Preserve the decode failure while still zeroing the disposable copy.
      }
      throw new Error('Protected refresh token could not be decoded.', {cause: error});
    } finally {
      encoded.fill(0);
    }
  }

  async clear(): Promise<void> {
    this.#tokens = null;
    await this.#storage.remove(this.#key);
  }
}

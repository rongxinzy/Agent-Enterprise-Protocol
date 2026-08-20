import type {AepTokens, AepTokenStore} from './types.js';

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

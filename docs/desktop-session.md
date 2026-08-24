# Desktop Main-Process Session Integration

The desktop application must construct `AepClient` in its trusted main process. The renderer may request login, logout, and AEP-backed operations through narrow IPC commands, but it must never receive the password, access token, model token, refresh token, or resolved Credential value.

`ProtectedRefreshTokenStore` keeps the complete token set in process memory and sends only the refresh token to an injected `AepProtectedStorage`. After a process restart, `restoreSession()` exchanges that protected refresh token for new short-lived tokens. Concurrent restoration and authenticated requests share the SDK refresh single-flight.

The `agentId` is not a secret, but it must be generated once and persisted in the main-process application configuration. A refresh token is bound to that Agent ID, so generating a new ID on every launch prevents session restoration.

## Electron safeStorage Adapter

The adapter below stores only an Electron `safeStorage` ciphertext under `app.getPath('userData')`. It rejects Linux's `basic_text` fallback and writes through a temporary file before rename.

```ts
import {randomUUID} from 'node:crypto';
import {mkdir, readFile, rename, rm, writeFile} from 'node:fs/promises';
import path from 'node:path';
import {app, safeStorage} from 'electron';
import {
  AepClient,
  ProtectedRefreshTokenStore,
  type AepProtectedStorage,
} from '@aep/sdk-node';

class ElectronSafeStorage implements AepProtectedStorage {
  readonly #directory = path.join(app.getPath('userData'), 'aep-session');

  async read(key: string): Promise<Uint8Array | null> {
    this.#assertAvailable();
    try {
      const encrypted = await readFile(this.#file(key));
      return new TextEncoder().encode(safeStorage.decryptString(encrypted));
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === 'ENOENT') return null;
      throw error;
    }
  }

  async write(key: string, value: Uint8Array): Promise<void> {
    this.#assertAvailable();
    await mkdir(this.#directory, {recursive: true, mode: 0o700});
    const target = this.#file(key);
    const temporary = `${target}.${randomUUID()}.tmp`;
    const plaintext = new TextDecoder('utf-8', {fatal: true}).decode(value);
    const encrypted = safeStorage.encryptString(plaintext);
    try {
      await writeFile(temporary, encrypted, {flag: 'wx', mode: 0o600});
      await rename(temporary, target);
    } finally {
      encrypted.fill(0);
      await rm(temporary, {force: true});
    }
  }

  async remove(key: string): Promise<void> {
    await rm(this.#file(key), {force: true});
  }

  #file(key: string): string {
    if (!/^[a-z0-9.-]+$/i.test(key)) throw new Error('Invalid protected-storage key.');
    return path.join(this.#directory, `${key}.bin`);
  }

  #assertAvailable(): void {
    if (!safeStorage.isEncryptionAvailable()) throw new Error('Operating-system protected storage is unavailable.');
    if (process.platform === 'linux' && safeStorage.getSelectedStorageBackend() === 'basic_text') {
      throw new Error('Electron safeStorage resolved to the insecure basic_text backend.');
    }
  }
}

const tokenStore = new ProtectedRefreshTokenStore(new ElectronSafeStorage());
const client = new AepClient({
  baseUrl: process.env.AEP_BASE_URL!,
  agentId: await loadStableAgentId(),
  agentVersion: app.getVersion(),
  platform: process.platform === 'darwin' ? 'macos' : process.platform === 'win32' ? 'windows' : 'linux',
  tokenStore,
});

const state = await client.getSessionState();
if (state.status === 'recoverable') await client.restoreSession();
```

`AepProtectedStorage.write` must copy and protect the supplied bytes before its promise resolves. The SDK zeroes that byte array immediately afterward. Electron's string-based API may leave an immutable JavaScript string until garbage collection; keep the adapter in the main process, never log it, and do not expose it through IPC.

On logout, `AepClient.logout()` restores a cold session when needed, revokes the server refresh session, and removes protected local state. If restoration or revocation fails, it still clears local protected state and propagates the error so the application can report that remote revocation was not confirmed.

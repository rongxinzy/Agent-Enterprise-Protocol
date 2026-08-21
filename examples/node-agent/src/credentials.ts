import {randomUUID} from 'node:crypto';

import type {AepClient, CredentialMetadata, ResolvedCredential} from '@aep/sdk-node';

import type {AgentState} from './state.js';

type CredentialControlClient = Pick<AepClient, 'listAgentCredentials' | 'resolveAgentCredential'>;
type CredentialTelemetryState = Pick<AgentState, 'enqueueTelemetry'>;

interface CachedCredential {
  metadataRevision: string;
  material: ResolvedCredential;
  expiresAtMs: number;
}

export interface CredentialManagerOptions {
  maxCacheMs?: number;
  now?: () => number;
}

export interface CredentialUseResult<T> {
  credential: CredentialMetadata;
  result: T;
}

export class CredentialUnavailableError extends Error {
  readonly code = 'CREDENTIAL_NOT_AVAILABLE';

  constructor(credentialId: string) {
    super(`Credential ${credentialId} is not available to this Agent.`);
    this.name = 'CredentialUnavailableError';
  }
}

/** Keeps resolved material in bounded process memory and never delegates it to AgentState. */
export class CredentialManager {
  readonly #maxCacheMs: number;
  readonly #now: () => number;
  readonly #metadata = new Map<string, CredentialMetadata>();
  readonly #cache = new Map<string, CachedCredential>();
  readonly #resolutions = new Map<
    string,
    {metadataRevision: string; promise: Promise<ResolvedCredential>}
  >();
  #synchronization: Promise<CredentialMetadata[]> | null = null;

  constructor(
    private readonly client: CredentialControlClient,
    options: CredentialManagerOptions = {},
  ) {
    this.#maxCacheMs = options.maxCacheMs ?? 30_000;
    this.#now = options.now ?? Date.now;
    if (!Number.isFinite(this.#maxCacheMs) || this.#maxCacheMs <= 0) {
      throw new Error('Credential maxCacheMs must be a positive finite number.');
    }
  }

  synchronize(): Promise<CredentialMetadata[]> {
    if (this.#synchronization) return this.#synchronization;
    this.#synchronization = this.#synchronize();
    return this.#synchronization.finally(() => {
      this.#synchronization = null;
    });
  }

  async use<T>(
    credentialId: string,
    purpose: string,
    consumer: (material: Readonly<ResolvedCredential>) => Promise<T>,
  ): Promise<CredentialUseResult<T>> {
    await this.synchronize();
    const metadata = this.#metadata.get(credentialId);
    if (!metadata) throw new CredentialUnavailableError(credentialId);
    const material = await this.#material(metadata, purpose);
    return {credential: metadata, result: await consumer(material)};
  }

  clear(): void {
    this.#cache.clear();
    this.#metadata.clear();
    this.#resolutions.clear();
  }

  async #synchronize(): Promise<CredentialMetadata[]> {
    const {credentials} = await this.client.listAgentCredentials();
    const current = new Map(credentials.map(item => [item.id, item]));
    for (const [credentialId, cached] of this.#cache) {
      const metadata = current.get(credentialId);
      if (!metadata || metadata.updatedAt !== cached.metadataRevision) this.#cache.delete(credentialId);
    }
    this.#metadata.clear();
    for (const metadata of credentials) this.#metadata.set(metadata.id, metadata);
    return credentials;
  }

  async #material(metadata: CredentialMetadata, purpose: string): Promise<ResolvedCredential> {
    const now = this.#now();
    const cached = this.#cache.get(metadata.id);
    if (cached && cached.metadataRevision === metadata.updatedAt && cached.expiresAtMs > now) {
      return cached.material;
    }
    this.#cache.delete(metadata.id);

    const pending = this.#resolutions.get(metadata.id);
    if (pending?.metadataRevision === metadata.updatedAt) return pending.promise;
    const resolution = this.client.resolveAgentCredential(metadata.id, purpose);
    this.#resolutions.set(metadata.id, {metadataRevision: metadata.updatedAt, promise: resolution});
    try {
      const material = await resolution;
      const serverExpiry = material.expiresAt ? Date.parse(material.expiresAt) : Number.POSITIVE_INFINITY;
      const expiresAtMs = Math.min(now + this.#maxCacheMs, serverExpiry);
      if (expiresAtMs > now) {
        this.#cache.set(metadata.id, {
          metadataRevision: metadata.updatedAt,
          material,
          expiresAtMs,
        });
      }
      return material;
    } finally {
      if (this.#resolutions.get(metadata.id)?.promise === resolution) {
        this.#resolutions.delete(metadata.id);
      }
    }
  }
}

export interface CredentialHttpResult {
  credentialId: string;
  service: string;
  status: number;
}

export class CredentialHttpClient {
  constructor(
    private readonly credentials: CredentialManager,
    private readonly state: CredentialTelemetryState,
  ) {}

  async get(credentialId: string, url: string, purpose: string): Promise<CredentialHttpResult> {
    const startedAt = Date.now();
    try {
      const used = await this.credentials.use(credentialId, purpose, async material => {
        const response = await fetch(url, {
          method: 'GET',
          headers: {Authorization: `Bearer ${material.value}`},
          redirect: 'error',
        });
        if (!response.ok) throw new CredentialConsumerError(response.status);
        return response.status;
      });
      this.#queueTelemetry('credential.use.completed', 'success', credentialId, startedAt, {
        service: used.credential.service,
        status: used.result,
      });
      return {credentialId, service: used.credential.service, status: used.result};
    } catch (error) {
      this.#queueTelemetry('credential.use.failed', 'failure', credentialId, startedAt, {
        errorCode: credentialErrorCode(error),
        ...credentialErrorStatus(error),
      });
      throw error;
    }
  }

  #queueTelemetry(
    type: 'credential.use.completed' | 'credential.use.failed',
    result: 'success' | 'failure',
    credentialId: string,
    startedAt: number,
    data: Record<string, string | number>,
  ): void {
    this.state.enqueueTelemetry({
      eventId: randomUUID(),
      type,
      occurredAt: new Date().toISOString(),
      resource: {type: 'credential', id: credentialId},
      result,
      data: {durationMs: Math.max(0, Date.now() - startedAt), ...data},
    });
  }
}

class CredentialConsumerError extends Error {
  readonly code = 'CREDENTIAL_CONSUMER_REJECTED';

  constructor(readonly status: number) {
    super(`Credential consumer returned HTTP ${status}.`);
    this.name = 'CredentialConsumerError';
  }
}

function credentialErrorCode(error: unknown): string {
  if (error instanceof CredentialUnavailableError || error instanceof CredentialConsumerError) return error.code;
  return 'CREDENTIAL_USE_FAILED';
}

function credentialErrorStatus(error: unknown): {status?: number} {
  return error instanceof CredentialConsumerError ? {status: error.status} : {};
}

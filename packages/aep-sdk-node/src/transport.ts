import {HttpMethod} from './constants.js';
import type {AepRequest, AepResponse, AepTransport, JsonValue} from './types.js';

export interface FetchTransportOptions {
  defaultTimeoutMs?: number;
  maxRetries?: number;
  fetch?: typeof globalThis.fetch;
}

const RETRYABLE_STATUS = new Set([429, 502, 503, 504]);

function isBodyInit(value: BodyInit | JsonValue): value is BodyInit {
  return (
    typeof value === 'string' ||
    value instanceof ArrayBuffer ||
    ArrayBuffer.isView(value) ||
    value instanceof Blob ||
    value instanceof FormData ||
    value instanceof URLSearchParams ||
    value instanceof ReadableStream
  );
}

function delay(milliseconds: number): Promise<void> {
  return new Promise(resolve => setTimeout(resolve, milliseconds));
}

export class FetchTransport implements AepTransport {
  readonly #defaultTimeoutMs: number;
  readonly #maxRetries: number;
  readonly #fetch: typeof globalThis.fetch;

  constructor(options: FetchTransportOptions = {}) {
    this.#defaultTimeoutMs = options.defaultTimeoutMs ?? 15_000;
    this.#maxRetries = options.maxRetries ?? 2;
    this.#fetch = options.fetch ?? globalThis.fetch;
  }

  async request<T>(baseUrl: string, request: AepRequest): Promise<AepResponse<T>> {
    const retryAllowed = request.retry ?? request.method === HttpMethod.Get;
    let attempt = 0;

    while (true) {
      const controller = new AbortController();
      const timeout = setTimeout(() => controller.abort(), request.timeoutMs ?? this.#defaultTimeoutMs);
      const headers = new Headers(request.headers);
      let body: BodyInit | undefined;
      if (request.body !== undefined) {
        if (isBodyInit(request.body)) {
          body = request.body;
        } else {
          headers.set('Content-Type', 'application/json');
          body = JSON.stringify(request.body);
        }
      }

      try {
        const response = await this.#fetch(new URL(request.path, ensureTrailingSlash(baseUrl)), {
          method: request.method,
          headers,
          body,
          signal: controller.signal,
        });
        const parsed = await parseResponse<T>(response, request.responseType);
        if (retryAllowed && RETRYABLE_STATUS.has(response.status) && attempt < this.#maxRetries) {
          await delay(retryDelay(response, attempt));
          attempt += 1;
          continue;
        }
        return {status: response.status, headers: response.headers, data: parsed};
      } catch (error) {
        if (!retryAllowed || attempt >= this.#maxRetries || controller.signal.aborted) throw error;
        await delay(50 * 2 ** attempt);
        attempt += 1;
      } finally {
        clearTimeout(timeout);
      }
    }
  }
}

function ensureTrailingSlash(value: string): string {
  return value.endsWith('/') ? value : `${value}/`;
}

function retryDelay(response: Response, attempt: number): number {
  const retryAfter = Number(response.headers.get('Retry-After'));
  if (Number.isFinite(retryAfter) && retryAfter >= 0) return Math.min(retryAfter * 1000, 2_000);
  return 50 * 2 ** attempt;
}

async function parseResponse<T>(response: Response, responseType?: AepRequest['responseType']): Promise<T> {
  if (response.status === 204 || response.status === 304) {
    return undefined as T;
  }
  if (!response.ok) {
    const text = await response.text();
    if (!text) return undefined as T;
    const contentType = response.headers.get('Content-Type') ?? '';
    return (contentType.includes('json') ? JSON.parse(text) : text) as T;
  }
  if (responseType === 'empty') return undefined as T;
  if (responseType === 'bytes') return new Uint8Array(await response.arrayBuffer()) as T;
  const text = await response.text();
  if (!text) return undefined as T;
  const contentType = response.headers.get('Content-Type') ?? '';
  return (contentType.includes('json') ? JSON.parse(text) : text) as T;
}

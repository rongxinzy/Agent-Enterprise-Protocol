import type {JsonValue, Query} from './types.js';

export function segment(value: string): string {
  return encodeURIComponent(value);
}

export function query(values: Query): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(values)) {
    if (value !== undefined && value !== null) search.set(key, String(value));
  }
  return search.toString();
}

export function asJson(value: unknown): JsonValue {
  return value as JsonValue;
}

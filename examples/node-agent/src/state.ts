import {DatabaseSync} from 'node:sqlite';

import type {AepTokens, AepTokenStore, ControlEvent, JsonObject} from '@aep/sdk-node';

export interface InboxItem {
  deliveryId: string;
  event: ControlEvent;
  state: 'received' | 'running' | 'succeeded' | 'failed';
}

export class AgentState implements AepTokenStore {
  readonly #database: DatabaseSync;

  constructor(path: string) {
    this.#database = new DatabaseSync(path);
    this.#database.exec(`
      PRAGMA journal_mode=WAL;
      CREATE TABLE IF NOT EXISTS kv (key TEXT PRIMARY KEY, value TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS inbox (
        delivery_id TEXT PRIMARY KEY,
        payload TEXT NOT NULL,
        state TEXT NOT NULL,
        updated_at TEXT NOT NULL
      );
      CREATE TABLE IF NOT EXISTS outbox (
        event_id TEXT PRIMARY KEY,
        payload TEXT NOT NULL,
        created_at TEXT NOT NULL
      );
      CREATE TABLE IF NOT EXISTS managed_skills (
        skill_id TEXT PRIMARY KEY,
        version TEXT NOT NULL,
        sha256 TEXT NOT NULL,
        path TEXT NOT NULL,
        updated_at TEXT NOT NULL
      );
    `);
  }

  close(): void {
    this.#database.close();
  }

  async get(): Promise<AepTokens | null> {
    const row = this.#database.prepare('SELECT value FROM kv WHERE key=?').get('tokens') as {value: string} | undefined;
    return row ? (JSON.parse(row.value) as AepTokens) : null;
  }

  async set(tokens: AepTokens): Promise<void> {
    this.#database.prepare('INSERT INTO kv(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value').run('tokens', JSON.stringify(tokens));
  }

  async clear(): Promise<void> {
    this.#database.prepare('DELETE FROM kv WHERE key=?').run('tokens');
  }

  setValue(key: string, value: string): void {
    this.#database.prepare('INSERT INTO kv(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value').run(key, value);
  }

  getValue(key: string): string | null {
    const row = this.#database.prepare('SELECT value FROM kv WHERE key=?').get(key) as {value: string} | undefined;
    return row?.value ?? null;
  }

  persistInbox(event: ControlEvent): void {
    this.#database.prepare(`INSERT INTO inbox(delivery_id,payload,state,updated_at) VALUES(?,?,?,?) ON CONFLICT(delivery_id) DO NOTHING`).run(event.deliveryId, JSON.stringify(event), 'received', new Date().toISOString());
  }

  listPendingInbox(): InboxItem[] {
    const rows = this.#database.prepare(`SELECT delivery_id,payload,state FROM inbox WHERE state IN ('received','running','failed') ORDER BY updated_at`).all() as Array<{delivery_id: string; payload: string; state: InboxItem['state']}>;
    return rows.map(row => ({deliveryId: row.delivery_id, event: JSON.parse(row.payload) as ControlEvent, state: row.state}));
  }

  setInboxState(deliveryId: string, state: InboxItem['state']): void {
    this.#database.prepare('UPDATE inbox SET state=?,updated_at=? WHERE delivery_id=?').run(state, new Date().toISOString(), deliveryId);
  }

  enqueueTelemetry(event: JsonObject): void {
    const eventId = String(event.eventId);
    this.#database.prepare('INSERT INTO outbox(event_id,payload,created_at) VALUES(?,?,?) ON CONFLICT(event_id) DO NOTHING').run(eventId, JSON.stringify(event), new Date().toISOString());
  }

  listTelemetry(limit = 100): JsonObject[] {
    return (this.#database.prepare('SELECT payload FROM outbox ORDER BY created_at LIMIT ?').all(limit) as Array<{payload: string}>).map(row => JSON.parse(row.payload) as JsonObject);
  }

  removeTelemetry(eventIds: string[]): void {
    const remove = this.#database.prepare('DELETE FROM outbox WHERE event_id=?');
    this.#database.exec('BEGIN');
    try {
      for (const eventId of eventIds) remove.run(eventId);
      this.#database.exec('COMMIT');
    } catch (error) {
      this.#database.exec('ROLLBACK');
      throw error;
    }
  }

  managedSkills(): Array<{skillId: string; version: string; sha256: string; path: string}> {
    return this.#database.prepare('SELECT skill_id AS skillId,version,sha256,path FROM managed_skills ORDER BY skill_id').all() as Array<{skillId: string; version: string; sha256: string; path: string}>;
  }

  setManagedSkill(skillId: string, version: string, sha256: string, path: string): void {
    this.#database.prepare('INSERT INTO managed_skills(skill_id,version,sha256,path,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(skill_id) DO UPDATE SET version=excluded.version,sha256=excluded.sha256,path=excluded.path,updated_at=excluded.updated_at').run(skillId, version, sha256, path, new Date().toISOString());
  }

  removeManagedSkill(skillId: string): void {
    this.#database.prepare('DELETE FROM managed_skills WHERE skill_id=?').run(skillId);
  }
}

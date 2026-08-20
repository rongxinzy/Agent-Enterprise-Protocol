import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

import {afterEach, expect, test} from 'vitest';

import {AgentState} from '../src/state.js';

const directories: string[] = [];

afterEach(() => {
  for (const directory of directories.splice(0)) fs.rmSync(directory, {recursive: true, force: true});
});

test('persists inbox and outbox across restarts', () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'aep-agent-state-'));
  directories.push(directory);
  const database = path.join(directory, 'state.sqlite');
  const first = new AgentState(database);
  first.persistInbox({deliveryId: 'delivery-1', eventId: 'event-1', cursor: '1', type: 'skill.manifest.changed', scope: {type: 'global'}, task: {type: 'skill.reconcile'}, createdAt: new Date().toISOString(), expiresAt: new Date(Date.now() + 60_000).toISOString()});
  first.persistInbox({deliveryId: 'delivery-1', eventId: 'event-1', cursor: '1', type: 'skill.manifest.changed', scope: {type: 'global'}, task: {type: 'skill.reconcile'}, createdAt: new Date().toISOString(), expiresAt: new Date(Date.now() + 60_000).toISOString()});
  first.enqueueTelemetry({eventId: 'telemetry-1', type: 'skill.sync.completed'});
  first.enqueueTelemetry({eventId: 'telemetry-1', type: 'skill.sync.failed'});
  first.close();

  const second = new AgentState(database);
  expect(second.listPendingInbox()).toHaveLength(1);
  expect(second.listTelemetry()).toEqual([{eventId: 'telemetry-1', type: 'skill.sync.completed'}]);
  second.close();
});

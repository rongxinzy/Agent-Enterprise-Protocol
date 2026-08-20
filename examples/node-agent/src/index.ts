import fs from 'node:fs';
import path from 'node:path';

import {AepClient} from '@aep/sdk-node';

import {ExampleAgent} from './agent.js';
import {SkillReconciler} from './skills.js';
import {AgentState} from './state.js';

const dataDirectory = path.resolve(process.env.AEP_AGENT_DATA_DIR ?? '.aep-agent');
fs.mkdirSync(dataDirectory, {recursive: true});
const state = new AgentState(path.join(dataDirectory, 'agent.sqlite'));
const platform = normalizePlatform(process.platform);
const client = new AepClient({
  baseUrl: process.env.AEP_BASE_URL ?? 'http://localhost:8080',
  agentId: process.env.AEP_AGENT_ID ?? 'example-agent',
  agentVersion: '0.1.0',
  platform,
  tokenStore: state,
});
const reconciler = new SkillReconciler(client, state, path.join(dataDirectory, 'managed-skills'));
const agent = new ExampleAgent({
  client,
  state,
  reconciler,
  agentVersion: '0.1.0',
  platform,
  credentials: {
    enterpriseId: process.env.AEP_ENTERPRISE_ID ?? 'demo',
    username: required('AEP_USERNAME'),
    password: required('AEP_PASSWORD'),
  },
});

const command = process.argv[2] ?? 'once';
try {
  if (command === 'reconcile') {
    await agent.reconcileSkills();
  } else if (command === 'once') {
    await agent.runOnce();
  } else if (command === 'run') {
    const interval = Number(process.env.AEP_POLL_INTERVAL_MS ?? 30_000);
    while (true) {
      await agent.runOnce();
      await new Promise(resolve => setTimeout(resolve, interval));
    }
  } else {
    throw new Error(`Unknown command: ${command}`);
  }
} finally {
  state.close();
}

function required(key: string): string {
  const value = process.env[key];
  if (!value) throw new Error(`${key} is required`);
  return value;
}

function normalizePlatform(value: NodeJS.Platform): 'windows' | 'macos' | 'linux' {
  if (value === 'win32') return 'windows';
  if (value === 'darwin') return 'macos';
  return 'linux';
}

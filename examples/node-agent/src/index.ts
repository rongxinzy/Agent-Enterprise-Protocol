import fs from 'node:fs';
import path from 'node:path';

import {AepClient} from '@aep/sdk-node';

import {ExampleAgent} from './agent.js';
import {CredentialHttpClient, CredentialManager} from './credentials.js';
import {OpenAIModelClient} from './models.js';
import {SkillReconciler} from './skills.js';
import {AgentState} from './state.js';

const dataDirectory = path.resolve(process.env.AEP_AGENT_DATA_DIR ?? '.aep-agent');
fs.mkdirSync(dataDirectory, {recursive: true});
const state = new AgentState(path.join(dataDirectory, 'agent.sqlite'));
const client = new AepClient({
  baseUrl: process.env.AEP_BASE_URL ?? 'http://localhost:8080',
  tokenStore: state,
});
const reconciler = new SkillReconciler(client, state, path.join(dataDirectory, 'managed-skills'));
const agent = new ExampleAgent({
  client,
  state,
  reconciler,
  credentials: {
    deploymentId: process.env.AEP_DEPLOYMENT_ID ?? process.env.AEP_ENTERPRISE_ID ?? 'demo',
    username: required('AEP_USERNAME'),
    password: required('AEP_PASSWORD'),
  },
});
const models = new OpenAIModelClient(client, state, {
  timeoutMs: Number(process.env.AEP_MODEL_TIMEOUT_MS ?? 120_000),
});
const credentials = new CredentialManager(client, {
  maxCacheMs: Number(process.env.AEP_CREDENTIAL_CACHE_MS ?? 30_000),
});
const credentialHttp = new CredentialHttpClient(credentials, state);

const command = process.argv[2] ?? 'once';
try {
  if (command === 'reconcile') {
    await agent.reconcileSkills();
  } else if (command === 'chat') {
    await client.loginWithPassword({
      deploymentId: process.env.AEP_DEPLOYMENT_ID ?? process.env.AEP_ENTERPRISE_ID ?? 'demo',
      username: required('AEP_USERNAME'),
      password: required('AEP_PASSWORD'),
    });
    try {
      const result = await models.chat({
        prompt: required('AEP_CHAT_PROMPT'),
        modelId: process.env.AEP_MODEL_ID || undefined,
        stream: parseBoolean(process.env.AEP_CHAT_STREAM),
      });
      process.stdout.write(JSON.stringify(result) + '\n');
    } finally {
      await agent.flushTelemetry();
    }
  } else if (command === 'credential') {
    await client.loginWithPassword({
      deploymentId: process.env.AEP_DEPLOYMENT_ID ?? process.env.AEP_ENTERPRISE_ID ?? 'demo',
      username: required('AEP_USERNAME'),
      password: required('AEP_PASSWORD'),
    });
    try {
      const result = await credentialHttp.get(
        required('AEP_CREDENTIAL_ID'),
        required('AEP_CREDENTIAL_URL'),
        process.env.AEP_CREDENTIAL_PURPOSE ?? 'Reference Agent HTTP request',
      );
      process.stdout.write(JSON.stringify(result) + '\n');
    } finally {
      credentials.clear();
      await agent.flushTelemetry();
    }
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

function parseBoolean(value: string | undefined): boolean {
  if (!value) return false;
  if (value === 'true' || value === '1') return true;
  if (value === 'false' || value === '0') return false;
  throw new Error('AEP_CHAT_STREAM must be true, false, 1, or 0');
}

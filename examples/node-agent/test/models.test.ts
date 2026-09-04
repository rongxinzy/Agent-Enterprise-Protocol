import http from 'node:http';

import type {UserModel, ModelConnection} from '@aep/sdk-node';
import {afterEach, expect, test} from 'vitest';

import {OpenAIModelClient} from '../src/models.js';
import {AgentState} from '../src/state.js';

const servers: http.Server[] = [];

afterEach(async () => {
  await Promise.all(servers.splice(0).map(server => new Promise<void>(resolve => server.close(() => resolve()))));
});

test('selects the default model and uses the official OpenAI request path and bearer token', async () => {
  const requests: Array<{authorization?: string; body: Record<string, unknown>; url?: string}> = [];
  const baseUrl = await startServer(async (request, response) => {
    requests.push({
      authorization: request.headers.authorization,
      body: await readJson(request),
      url: request.url,
    });
    sendJson(response, 200, {
      id: 'completion-1',
      object: 'chat.completion',
      created: 1,
      model: 'upstream-default',
      choices: [{index: 0, message: {role: 'assistant', content: 'default reply'}, finish_reason: 'stop'}],
      usage: {prompt_tokens: 2, completion_tokens: 3, total_tokens: 5},
    });
  });
  const state = new AgentState(':memory:');
  const client = new OpenAIModelClient(
    controlClient([model('secondary'), model('default-model', true)], connection(baseUrl, 'short-lived-model-token')),
    state,
  );

  const result = await client.chat({prompt: 'do not record this prompt'});

  expect(result).toEqual({
    modelId: 'default-model',
    responseModel: 'upstream-default',
    content: 'default reply',
    streamed: false,
  });
  expect(requests).toEqual([
    {
      authorization: 'Bearer short-lived-model-token',
      body: {model: 'default-model', messages: [{role: 'user', content: 'do not record this prompt'}]},
      url: '/v1/chat/completions',
    },
  ]);
  const telemetry = state.listTelemetry();
  expect(telemetry).toHaveLength(1);
  expect(telemetry[0]).toMatchObject({
    type: 'model.request.completed',
    resource: {type: 'model', id: 'default-model'},
    result: 'success',
    data: {
      modelId: 'default-model',
      responseModel: 'upstream-default',
      streamed: false,
      promptTokens: 2,
      completionTokens: 3,
      totalTokens: 5,
    },
  });
  expect(JSON.stringify(telemetry)).not.toContain('do not record this prompt');
  expect(JSON.stringify(telemetry)).not.toContain('short-lived-model-token');
  state.close();
});

test('selects an explicit model and aggregates streaming content', async () => {
  const baseUrl = await startServer(async (request, response) => {
    const body = await readJson(request);
    expect(body).toMatchObject({model: 'stream-model', stream: true});
    response.writeHead(200, {'Content-Type': 'text/event-stream'});
    response.write(`data: ${JSON.stringify(chunk('Hello'))}\n\n`);
    response.write(`data: ${JSON.stringify(chunk(' AEP'))}\n\n`);
    response.end('data: [DONE]\n\n');
  });
  const state = new AgentState(':memory:');
  const client = new OpenAIModelClient(
    controlClient([model('default-model', true), model('stream-model')], connection(baseUrl, 'token')),
    state,
  );

  await expect(client.chat({modelId: 'stream-model', prompt: 'stream', stream: true})).resolves.toEqual({
    modelId: 'stream-model',
    responseModel: 'upstream-stream',
    content: 'Hello AEP',
    streamed: true,
  });
  expect(state.listTelemetry()[0]).toMatchObject({
    type: 'model.request.completed',
    resource: {id: 'stream-model'},
    data: {streamed: true},
  });
  state.close();
});

test('persists redacted failure telemetry for an upstream error', async () => {
  const baseUrl = await startServer(async (request, response) => {
    await readJson(request);
    sendJson(response, 503, {error: {message: 'provider secret failure detail', type: 'upstream_error'}});
  });
  const state = new AgentState(':memory:');
  const client = new OpenAIModelClient(
    controlClient([model('default-model', true)], connection(baseUrl, 'never-log-this-token')),
    state,
  );

  await expect(client.chat({prompt: 'never-log-this-prompt'})).rejects.toMatchObject({status: 503});
  const telemetry = state.listTelemetry();
  expect(telemetry).toHaveLength(1);
  expect(telemetry[0]).toMatchObject({
    type: 'model.request.failed',
    resource: {type: 'model', id: 'default-model'},
    result: 'failure',
    data: {
      modelId: 'default-model',
      streamed: false,
      errorCode: 'MODEL_REQUEST_FAILED',
      status: 503,
    },
  });
  const serialized = JSON.stringify(telemetry);
  expect(serialized).not.toContain('provider secret failure detail');
  expect(serialized).not.toContain('never-log-this-token');
  expect(serialized).not.toContain('never-log-this-prompt');
  expect(serialized).not.toContain(baseUrl);
  state.close();
});

function model(id: string, isDefault = false): UserModel {
  return {
    id,
    displayName: id,
    sourceType: 'gateway',
    protocol: 'openai-compatible',
    capabilities: ['text', 'streaming'],
    isDefault,
    enabled: true,
  };
}

function connection(baseUrl: string, apiKey: string): ModelConnection {
  return {
    baseUrl: `${baseUrl}/v1`,
    apiKey,
    expiresIn: 900,
    protocol: 'openai-compatible',
  };
}

function controlClient(models: UserModel[], modelConnection: ModelConnection) {
  return {
    async listModels() {
      return {models};
    },
    async getModelConnection() {
      return modelConnection;
    },
  };
}

async function startServer(handler: (request: http.IncomingMessage, response: http.ServerResponse) => Promise<void>): Promise<string> {
  const server = http.createServer((request, response) => {
    void handler(request, response).catch(error => {
      response.destroy(error instanceof Error ? error : new Error(String(error)));
    });
  });
  servers.push(server);
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  const address = server.address();
  if (!address || typeof address === 'string') throw new Error('Mock OpenAI server did not bind a TCP port.');
  return `http://127.0.0.1:${address.port}`;
}

async function readJson(request: http.IncomingMessage): Promise<Record<string, unknown>> {
  const chunks: Buffer[] = [];
  for await (const value of request) chunks.push(Buffer.from(value));
  return JSON.parse(Buffer.concat(chunks).toString('utf8')) as Record<string, unknown>;
}

function sendJson(response: http.ServerResponse, status: number, value: unknown): void {
  response.writeHead(status, {'Content-Type': 'application/json'});
  response.end(JSON.stringify(value));
}

function chunk(content: string) {
  return {
    id: 'completion-stream',
    object: 'chat.completion.chunk',
    created: 1,
    model: 'upstream-stream',
    choices: [{index: 0, delta: {content}, finish_reason: null}],
  };
}

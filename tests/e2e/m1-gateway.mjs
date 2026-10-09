import {spawn} from 'node:child_process';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

import {AepClient, MemoryTokenStore} from '../../packages/aep-sdk-node/dist/index.js';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const composeFiles = [
  path.join(root, 'deploy', 'compose', 'compose.yaml'),
  path.join(root, 'deploy', 'compose', 'gateway.yaml'),
  path.join(root, 'tests', 'e2e', 'fixtures', 'higress-monitoring.compose.yaml'),
];
const project = 'aep-m1-gateway-e2e';
const controlPort = process.env.AEP_M1_GATEWAY_CONTROL_PORT ?? '18083';
const gatewayPort = process.env.AEP_M1_GATEWAY_PORT ?? '19080';
const controlBaseUrl = 'http://localhost:' + controlPort;
const gatewayBaseUrl = 'http://localhost:' + gatewayPort + '/v1';
const prometheusPort = process.env.AEP_M1_GATEWAY_PROMETHEUS_PORT ?? '19091';
const prometheusBaseUrl = 'http://localhost:' + prometheusPort;
const composeEnv = {
  AEP_PORT: controlPort,
  AEP_GATEWAY_PORT: gatewayPort,
  AEP_MINIO_CONSOLE_PORT: process.env.AEP_M1_GATEWAY_MINIO_CONSOLE_PORT ?? '19003',
  AEP_MODEL_ACCESS_TTL: process.env.AEP_M1_GATEWAY_TOKEN_TTL ?? '8s',
  AEP_PROMETHEUS_PORT: prometheusPort,
};
const runId = Date.now().toString(36);

try {
  // Compose can report a transient dependency failure while the control service
  // restarts; the health checks below are the authoritative readiness gate.
  await compose('up', '-d', '--build', true);
  await Promise.all([
    waitForHealth(controlBaseUrl + '/healthz', 180_000),
    waitForHealth('http://localhost:' + gatewayPort + '/healthz', 180_000),
    waitForHealth(prometheusBaseUrl + '/-/ready', 180_000),
  ]);
  await runScenario();
  console.log('AEP M1 Higress gateway scenario passed.');
} catch (error) {
  await compose('logs', '--no-color', '--tail=200', 'higress', 'gateway-authorizer', 'mock-openai', 'prometheus', true);
  throw error;
} finally {
  await compose('down', '-v', '--remove-orphans', true);
}

async function runScenario() {
  const admin = new AepClient({
    baseUrl: controlBaseUrl, tokenStore: new MemoryTokenStore(),
  });
  await admin.loginWithPassword({deploymentId: 'demo', username: 'admin', password: 'change-this-admin-password'});
  const modelCredential = await admin.createCredential({
    name: 'M1 gateway provider', service: 'mock-openai', type: 'api_key',
    deliveryMode: 'server_only', value: 'm1-e2e-provider-secret', enabled: true,
  });
  const connection = await admin.getModelConnection();
  assert(connection.baseUrl === gatewayBaseUrl, 'Expected gateway URL ' + gatewayBaseUrl + ', got ' + connection.baseUrl);

  const username = 'gateway-user-' + runId;
  const password = 'temporary-password-123';
  const user = await admin.createUser({
    deploymentId: 'demo', username, displayName: 'Gateway User ' + runId,
    temporaryPassword: password, requirePasswordChange: false, teamIds: ['all-users'], roleIds: ['admin'],
  });
  await admin.createModel({
    displayName: 'Enterprise Chat', sourceType: 'gateway',
    protocol: 'openai-compatible', endpoint: gatewayBaseUrl, upstreamModel: 'mock-upstream-chat',
    credentialId: modelCredential.id, capabilities: ['text', 'streaming', 'reasoning'],
    reasoningCompatibility: {
      thinkingFormat: 'deepseek', supportsReasoningEffort: true,
      requiresReasoningContentOnAssistantMessages: true,
    },
    contextWindow: 32768,
    isDefault: true, enabled: true,
  });
  await admin.createModel({
    displayName: 'Unassigned Chat', sourceType: 'gateway',
    protocol: 'openai-compatible', endpoint: gatewayBaseUrl, upstreamModel: 'unassigned-upstream',
    credentialId: null, capabilities: ['text'], contextWindow: 8192,
    isDefault: false, enabled: true,
  });
  await admin.createModel({
    displayName: 'Bench Anthropic', sourceType: 'gateway',
    protocol: 'anthropic', endpoint: 'http://mock-openai.aep.internal:8080/api/anthropic', upstreamModel: 'mock-upstream-chat',
    credentialId: modelCredential.id, capabilities: ['text'],
    isDefault: false, enabled: true,
  });
  await admin.createModelAssignment({modelId: 'enterprise-chat', subject: {type: 'user', id: user.id}});
  await admin.createModelAssignment({modelId: 'bench-anthropic', subject: {type: 'user', id: user.id}});

  const store = new MemoryTokenStore();
  const agent = new AepClient({
    baseUrl: controlBaseUrl, tokenStore: store,
  });
  await agent.loginWithPassword({deploymentId: 'demo', username, password});
  const tokens = await store.get();
  const modelToken = tokens?.modelAccessToken;
  assert(modelToken, 'Agent login did not return a model access token');

  const completion = await inference(modelToken, {model: 'enterprise-chat', messages: [{role: 'user', content: 'hello'}]});
  assert(completion.response.status === 200, 'Non-streaming inference failed: ' + completion.response.status + ' ' + completion.text);
  const completionBody = JSON.parse(completion.text);
  assert(completionBody.model === 'mock-upstream-chat', 'Higress did not rewrite the enterprise model ID');
  assert(completionBody.choices[0].message.content === 'Hello AEP', 'Unexpected mock completion content');
  assert(completionBody.choices[0].message.reasoning_content === 'Think through the request.', 'Non-stream reasoning_content was lost');
  assert(completion.response.headers.get('x-mock-provider-auth') === 'accepted', 'Higress did not inject the provider credential');
  assert(!containsSecret(completion), 'Provider credentials were exposed in the client response');

  const streaming = await inference(modelToken, {model: 'enterprise-chat', stream: true, stream_options: {include_usage: true}, messages: [{role: 'user', content: 'stream'}]});
  assert(streaming.response.status === 200, 'Streaming inference failed: ' + streaming.response.status + ' ' + streaming.text);
  assert(streaming.response.headers.get('content-type')?.startsWith('text/event-stream'), 'Streaming response was not SSE');
  assert(streaming.text.includes('Hello') && streaming.text.includes(' AEP') && streaming.text.includes('[DONE]'), 'Streaming chunks were incomplete');
  assert(streaming.text.includes('"finish_reason":"stop"'), 'Streaming response did not include a terminal finish reason');
  assert(streaming.text.includes('reasoning_content') && streaming.text.includes('Think through the request.'), 'Streaming reasoning_content was lost');
  assert(!containsSecret(streaming), 'Provider credentials were exposed in the streaming response');

  const replay = await inference(modelToken, {
    model: 'enterprise-chat', thinking: {type: 'enabled'}, reasoning_effort: 'high',
    messages: [
      {role: 'user', content: 'verify reasoning replay'},
      {role: 'assistant', content: null, reasoning_content: 'Call the clock tool.', tool_calls: [{id: 'call-1', type: 'function', function: {name: 'clock', arguments: '{}'}}]},
      {role: 'tool', tool_call_id: 'call-1', content: '12:00'},
    ],
  });
  assert(replay.response.status === 200, 'Reasoning tool replay failed: ' + replay.response.status + ' ' + replay.text);

  // Anthropic passthrough: per-model path prefix, EnvoyFilter (not ai-proxy)
  // hosts the route, body flows verbatim, credential injected server-side.
  const anthropic = await anthropicInference(modelToken, {model: 'bench-anthropic', max_tokens: 64, messages: [{role: 'user', content: 'passthrough please'}]});
  assert(anthropic.response.status === 200, 'Anthropic passthrough failed: ' + anthropic.response.status + ' ' + anthropic.text);
  const anthropicBody = JSON.parse(anthropic.text);
  assert(anthropicBody.type === 'message' && anthropicBody.model === 'bench-anthropic', 'Anthropic body did not pass through verbatim: ' + anthropic.text);
  assert(anthropicBody.content?.[0]?.text === 'anthropic passthrough ok bench-anthropic', 'Unexpected anthropic mock reply: ' + anthropic.text);
  assert(!containsSecret(anthropic), 'Provider credentials were exposed in the anthropic passthrough response');

  const failed = await inference(modelToken, {model: 'enterprise-chat', messages: [{role: 'user', content: 'force upstream failure'}]});
  assert(failed.response.status === 503, 'Upstream failure status was not preserved');

  await expectGatewayProblem(null, {model: 'enterprise-chat'}, 401, 'TOKEN_INVALID');
  await expectGatewayProblem(modelToken + 'invalid', {model: 'enterprise-chat'}, 401, 'TOKEN_INVALID');
  await expectGatewayProblem(modelToken, {model: 'unassigned-chat'}, 403, 'MODEL_NOT_ALLOWED');

  const expiresAt = decodeJwtPart(modelToken, 1).exp * 1000;
  await new Promise(resolve => setTimeout(resolve, Math.max(0, expiresAt - Date.now() + 250)));
  await expectGatewayProblem(modelToken, {model: 'enterprise-chat'}, 401, 'TOKEN_INVALID');
  await verifyStatistics(modelToken);
  await verifyPrometheus();
  await verifyGatewayManagement(agent, user.id);
}

async function verifyGatewayManagement(client, userId) {
  const access = await client.createGatewayTestAccess('enterprise-chat');
  const claims = decodeJwtPart(access.modelAccessToken, 1);
  assert(access.baseUrl === gatewayBaseUrl && access.path === '/chat/completions', 'Test access did not use the configured gateway');
  assert(claims.sub === userId && claims.model_scopes.length === 1 && claims.model_scopes[0] === 'enterprise-chat' && claims.exp - claims.iat <= 120, 'Test access is not bound to one model/session with a two-minute limit');
  assert(Date.parse(access.expiresAt) === claims.exp * 1_000, 'Test access expiry disagrees with the JWT');
  const completion = await inference(access.modelAccessToken, {model: 'enterprise-chat', messages: [{role: 'user', content: 'test access'}]});
  assert(completion.response.status === 200 && !containsSecret(completion), 'Test access failed through authorizer and Higress');
  const streaming = await inference(access.modelAccessToken, {model: 'enterprise-chat', stream: true, stream_options: {include_usage: true}, messages: [{role: 'user', content: 'stream'}]});
  assert(streaming.response.status === 200 && streaming.text.includes('[DONE]') && streaming.text.includes('reasoning_content'), 'Test access did not preserve SSE/reasoning');
  await expectGatewayProblem(access.modelAccessToken, {model: 'bench-anthropic'}, 403, 'MODEL_NOT_ALLOWED');
  await expectAepProblem(() => client.createGatewayTestAccess('unassigned-chat'), 403);
  const anthropicAccess = await client.createGatewayTestAccess('bench-anthropic');
  assert(anthropicAccess.baseUrl === 'http://localhost:' + gatewayPort + '/bench-anthropic' && anthropicAccess.path === '/v1/messages', 'Anthropic test access lost the per-model route');
  const anthropic = await anthropicInference(anthropicAccess.modelAccessToken, {model: 'bench-anthropic', max_tokens: 64, messages: [{role: 'user', content: 'test access'}]});
  assert(anthropic.response.status === 200 && !containsSecret(anthropic), 'Anthropic test access failed');

  const capabilities = await client.getGatewayCapabilities();
  assert(capabilities.sources.prometheus && capabilities.sources.testAccess && !capabilities.sources.loki && capabilities.unsupported.includes('cost') && capabilities.unsupported.includes('p95'), 'Gateway capabilities do not reflect configured native sources');
  const health = await client.getGatewayMonitoringHealth();
  assert(health.sources.some(source => source.source === 'prometheus' && source.state === 'healthy' && source.targets.length === 1), 'Native gateway target health is missing');
  const end = new Date().toISOString();
  const start = new Date(Date.now() - 60_000).toISOString();
  const definitions = {
    input_tokens: 'ai_input_tokens', output_tokens: 'ai_output_tokens',
    calls: 'ai_usage_completed_calls', failures: 'ai_detected_failures',
    first_token_duration: 'ai_usage_mean_first_token_duration', service_duration: 'ai_usage_mean_service_duration',
  };
  for (const [metric, expectedDefinition] of Object.entries(definitions)) {
    const result = await client.queryGatewayMetrics({metric, start, end, step: 10, groupBy: 'user', userId, expectedDefinition});
    assert(result.source === 'prometheus' && result.data.status === 'success' && result.data.data.resultType === 'matrix', 'Gateway metrics did not return native Prometheus data: ' + metric);
    const rateMean = metric === 'first_token_duration' || metric === 'service_duration';
    assert(result.definition.id === expectedDefinition && result.definition.windowSeconds === (rateMean ? 120 : 10)
      && result.definition.groupBy === 'user' && result.definition.modelDimension === 'not_applicable', 'Gateway metric definition does not describe the native query: ' + metric);
  }
  await expectAepProblem(() => client.queryGatewayMetrics({metric: 'calls', start, end,
    modelId: 'enterprise-chat', expectedDefinition: 'ai_usage_completed_calls'}), 422, 'GATEWAY_METRIC_DEFINITION_MISMATCH');
  await expectAepProblem(() => client.searchGatewayRequests({start, end, limit: 10}), 503);
  await expectAepProblem(() => client.queryGatewayMetrics({metric: 'calls', start, end, groupBy: 'team'}), 422);

  const id = 'test-limit-' + runId;
  const rule = {kind: 'requests', scopeType: 'user', scopeId: userId, maximum: 20, interval: 'minute', enabled: true, expectedVersion: 0};
  const created = await client.putGatewayLimit(id, rule);
  assert(created.version === 1 && (await client.getGatewayLimit(id)).configuration.scopeId === userId, 'Gateway rule CRUD failed');
  await expectAepProblem(() => client.putGatewayLimit(id, rule), 409);
  const updated = await client.putGatewayLimit(id, {...rule, maximum: 30, expectedVersion: 1});
  assert(updated.version === 2, 'Gateway rule update did not advance the version');
  const publication = await client.publishGatewayLimits();
  const status = await client.getGatewayLimitsStatus();
  assert(publication.revision === status.revision && status.state === 'pending' && !status.runtimeVerified, 'Configuration publication claimed unverified runtime enforcement');
  await client.deleteGatewayLimit(id, updated.version);
  assert(!(await client.listGatewayLimits()).items.some(item => item.id === id), 'Deleted gateway rule remains visible');
  await client.publishGatewayLimits();
  await expectAepProblem(() => client.getGatewayQuota(userId), 503);
  console.log('Gateway management APIs passed with PostgreSQL, native Higress inference/identity and Prometheus; unconfigured sources remain unavailable.');
}

async function expectAepProblem(action, status, code) {
  try {
    await action();
  } catch (error) {
    assert(error.status === status, 'Expected AEP status ' + status + ', got ' + error.status);
    if (code) assert(error.code === code, 'Expected AEP problem ' + code + ', got ' + error.code);
    return;
  }
  throw new Error('Expected AEP status ' + status);
}

async function verifyPrometheus() {
  const deadline = Date.now() + 45_000;
  let lastError;
  while (Date.now() < deadline) {
    try {
      const targetsResponse = await fetch(prometheusBaseUrl + '/api/v1/targets', {signal: AbortSignal.timeout(5_000)});
      assert(targetsResponse.ok, 'Prometheus targets API failed');
      const targets = (await targetsResponse.json()).data.activeTargets;
      assert(targets.length === 1 && targets[0].health === 'up', 'Expected one healthy Higress scrape target');
      const selector = 'job="higress-gateway",higress="higress-system-higress-gateway"';
      const input = 'route_upstream_model_consumer_metric_input_token';
      const output = 'route_upstream_model_consumer_metric_output_token';
      assert(await prometheusValue(`sum(${input}{${selector},ai_route="aep-model-gateway"})`) >= 3, 'Prometheus did not store OpenAI/SSE input usage');
      assert(await prometheusValue(`sum(${output}{${selector},ai_route="aep-model-gateway"})`) >= 6, 'Prometheus did not store OpenAI/SSE output usage');
      assert(await prometheusValue(`sum(${input}{${selector},ai_route="aep-anthropic-bench-anthropic"})`) >= 1, 'Prometheus did not store Anthropic input usage');
      assert(await prometheusValue(`sum(route_upstream_model_consumer_metric_llm_failure_count{${selector}})`) >= 1, 'Prometheus did not store the upstream failure counter');
      const end = Date.now() / 1_000;
      const params = new URLSearchParams({query: `sum(${input}{${selector}})`, start: String(end - 10), end: String(end), step: '1'});
      const rangeResponse = await fetch(prometheusBaseUrl + '/api/v1/query_range?' + params, {signal: AbortSignal.timeout(5_000)});
      assert(rangeResponse.ok, 'Prometheus range API failed');
      const range = await rangeResponse.json();
      assert(range.status === 'success' && range.data.resultType === 'matrix' && range.data.result.some(series => series.values.length >= 2), 'Stored usage did not produce a time series');
      console.log('Prometheus scraped one Higress target and queried stored OpenAI/SSE/Anthropic usage, failures and time series.');
      return;
    } catch (error) {
      lastError = error;
      await new Promise(resolve => setTimeout(resolve, 1_000));
    }
  }
  throw lastError;
}

async function prometheusValue(query) {
  const response = await fetch(prometheusBaseUrl + '/api/v1/query?' + new URLSearchParams({query}), {signal: AbortSignal.timeout(5_000)});
  assert(response.ok, 'Prometheus query API failed');
  const body = await response.json();
  assert(body.status === 'success' && body.data.resultType === 'vector' && body.data.result.length === 1, 'Prometheus query returned no data: ' + query);
  return Number(body.data.result[0].value[1]);
}

async function verifyStatistics(modelToken) {
  const deadline = Date.now() + 45_000;
  let lastError;
  while (Date.now() < deadline) {
    try {
      const metrics = await composeOutput('exec', '-T', 'higress', 'curl', '-fsS', '--max-time', '5', 'http://localhost:15020/stats/prometheus');
      const value = (name, route) => metrics.split('\n').filter(line => line.startsWith(name + '{') && line.includes(`ai_route="${route}"`)).reduce((sum, line) => sum + Number(line.slice(line.lastIndexOf(' ') + 1)), 0);
      const prefix = 'route_upstream_model_consumer_metric_';
      assert(value(prefix + 'input_token', 'aep-model-gateway') >= 3, 'OpenAI input tokens were not exported, including streaming usage');
      assert(value(prefix + 'output_token', 'aep-model-gateway') >= 6, 'OpenAI output tokens were not exported');
      assert(value(prefix + 'llm_stream_duration_count', 'aep-model-gateway') >= 1, 'Streaming observations were not exported');
      assert(value(prefix + 'input_token', 'aep-anthropic-bench-anthropic') >= 1, 'Anthropic input tokens were not exported');
      assert(value(prefix + 'output_token', 'aep-anthropic-bench-anthropic') >= 2, 'Anthropic output tokens were not exported');
      assert(!metrics.includes(modelToken) && !metrics.includes('m1-e2e-provider-secret') && !metrics.includes('Think through the request.'), 'Model content or credentials leaked into metrics');
      console.log('Higress AI statistics exported OpenAI, SSE and Anthropic usage without model content or credentials.');
      return;
    } catch (error) {
      lastError = error;
      await new Promise(resolve => setTimeout(resolve, 1_000));
    }
  }
  throw lastError;
}

function composeOutput(...args) {
  return new Promise((resolve, reject) => {
    const commandArgs = ['compose', '-p', project];
    for (const file of composeFiles) commandArgs.push('-f', file);
    commandArgs.push(...args);
    const child = spawn('docker', commandArgs, {cwd: root, env: {...process.env, ...composeEnv}, stdio: ['ignore', 'pipe', 'pipe'], shell: false});
    let result = '';
    child.stdout.on('data', data => { result += data; });
    child.stderr.resume();
    child.on('error', reject);
    child.on('exit', code => code === 0 ? resolve(result) : reject(new Error('Higress metrics command failed with exit ' + code)));
  });
}

async function inference(token, body) {
  const headers = {
    'Content-Type': 'application/json',
    'X-AEP-Deployment-ID': 'spoofed',
    'X-AEP-License-ID': 'spoofed-license',
    'X-AEP-Internal-Role': 'admin',
  };
  if (token) headers.Authorization = 'Bearer ' + token;
  const response = await fetch(gatewayBaseUrl + '/chat/completions', {method: 'POST', headers, body: JSON.stringify(body)});
  return {response, text: await response.text()};
}

async function anthropicInference(token, body) {
  const headers = {
    'Content-Type': 'application/json',
    'Authorization': 'Bearer ' + token,
    'X-AEP-Internal-Role': 'admin',
  };
  const base = 'http://localhost:' + gatewayPort;
  const response = await fetch(base + '/bench-anthropic/v1/messages', {method: 'POST', headers, body: JSON.stringify(body)});
  return {response, text: await response.text()};
}

async function expectGatewayProblem(token, body, status, code) {
  const result = await inference(token, body);
  let problem;
  try { problem = JSON.parse(result.text); } catch {}
  assert(result.response.status === status && problem?.code === code, 'Expected ' + status + ' ' + code + ', got ' + result.response.status + ' ' + result.text);
}

function containsSecret(result) {
  const headers = [...result.response.headers].map(([name, value]) => name + ':' + value).join('\n');
  return (headers + '\n' + result.text).includes('m1-e2e-provider-secret');
}

function decodeJwtPart(token, index) {
  return JSON.parse(Buffer.from(token.split('.')[index], 'base64url').toString('utf8'));
}

async function waitForHealth(url, timeout) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url);
      if (response.ok) return;
    } catch {}
    await new Promise(resolve => setTimeout(resolve, 1_000));
  }
  throw new Error(url + ' did not become healthy within ' + timeout + 'ms');
}

function compose(...args) {
  let allowFailure = false;
  if (args.at(-1) === true) {
    allowFailure = true;
    args.pop();
  }
  const composeArgs = ['compose', '-p', project];
  for (const file of composeFiles) composeArgs.push('-f', file);
  composeArgs.push(...args);
  return command('docker', composeArgs, composeEnv, allowFailure);
}

function command(executable, args, extraEnv = {}, allowFailure = false) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {cwd: root, env: {...process.env, ...extraEnv}, stdio: 'inherit', shell: false});
    child.on('error', reject);
    child.on('exit', code => {
      if (code === 0 || allowFailure) resolve();
      else reject(new Error(executable + ' ' + args.join(' ') + ' exited with ' + code));
    });
  });
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function platform() {
  if (process.platform === 'win32') return 'windows';
  if (process.platform === 'darwin') return 'macos';
  return 'linux';
}

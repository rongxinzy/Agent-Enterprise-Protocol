import {spawn} from 'node:child_process';
import {createHash} from 'node:crypto';
import {mkdtemp, mkdir, readFile, writeFile} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import yaml from 'js-yaml';

import {AepClient, MemoryTokenStore} from '../../packages/aep-sdk-node/dist/index.js';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const work = await mkdtemp(path.join(tmpdir(), 'aep-native-e2e-'));
const project = 'aep-gateway-native-e2e';
const controlPort = process.env.AEP_NATIVE_CONTROL_PORT ?? '18084';
const gatewayPort = process.env.AEP_NATIVE_GATEWAY_PORT ?? '19081';
const env = {...process.env, AEP_PORT: controlPort, AEP_GATEWAY_PORT: gatewayPort,
  AEP_MINIO_CONSOLE_PORT: process.env.AEP_NATIVE_MINIO_CONSOLE_PORT ?? '19004', AEP_MODEL_ACCESS_TTL: '15m',
  AEP_CONTROL_SERVICE_IMAGE: project + '-control-service', AEP_GATEWAY_AUTHORIZER_IMAGE: project + '-gateway-authorizer',
  AEP_GATEWAY_RECONCILER_IMAGE: project + '-gateway-reconciler'};
const control = 'http://127.0.0.1:' + controlPort;
const gateway = 'http://127.0.0.1:' + gatewayPort;
const suffix = 'demo-' + createHash('sha256').update('demo').digest('hex').slice(0, 8);
const quotaPath = `/aep-quota-${suffix}/v1/chat/completions/quota`;
const pluginDir = path.join(work, 'wasmplugins');
const ingressDir = path.join(work, 'ingresses');
const files = [path.join(root, 'deploy/compose/compose.yaml'), path.join(root, 'deploy/compose/gateway.yaml'), path.join(work, 'override.json')];
const desired = {deploymentId: 'demo', routes: [
  {modelId: 'enterprise-chat', enabled: true, protocol: 'openai-compatible'},
  {modelId: 'bench-anthropic', enabled: true, protocol: 'anthropic'},
]};
let rules = [];
let running = false;

function assert(ok, message) { if (!ok) throw new Error(message); }
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(action, timeout = 120_000) {
  const end = Date.now() + timeout;
  let last;
  while (Date.now() < end) { try { return await action(); } catch (error) { last = error; await delay(1000); } }
  throw last;
}
async function command(name, args, {input, allowFailure = false} = {}) {
  return await new Promise((resolve, reject) => {
    const child = spawn(name, args, {cwd: root, env, stdio: ['pipe', 'pipe', 'pipe'], shell: false});
    let out = '', errors = '';
    child.stdout.on('data', value => { out += value; });
    child.stderr.on('data', value => { errors += value; });
    child.on('error', reject);
    child.on('exit', code => code === 0 || allowFailure ? resolve(out) : reject(new Error(name + ' exited ' + code + ': ' + errors.slice(-2000))));
    child.stdin.end(input);
  });
}
const compose = (...args) => command('docker', ['compose', '-p', project, ...files.flatMap(file => ['-f', file]), ...args]);
const higress = (...args) => command('docker', ['exec', project + '-higress-1', ...args]);
const redis = (...args) => command('docker', ['exec', project + '-redis-1', 'redis-cli', ...args]);

async function ready() {
  await until(async () => {
    await higress('curl', '-fsS', '--max-time', '5', 'http://127.0.0.1:15021/healthz/ready');
    const response = await higress('curl', '-sS', '--max-time', '5', '-w', '%{http_code}', '-H',
      'Authorization: Bearer disposable-native-e2e-admin', 'http://127.0.0.1:8080' + quotaPath + '?consumer=probe');
    assert(response.endsWith('200'), 'Native quota route is not ready');
  });
  await delay(7000);
}

async function render() {
  const resources = JSON.parse(await command('go', ['run', './services/gateway-reconciler/internal/reconciler/testdata/render-native'],
    {input: JSON.stringify({desired, items: rules})}));
  for (const resource of resources) {
    const doc = yaml.load(resource.Body);
    // Adapt production resource names to the existing standalone fixture only.
    if (doc.kind === 'Ingress') doc.metadata.annotations = {'higress.io/destination': 'mock-openai.dns'};
    for (const match of doc.spec.matchRules ?? []) match.ingress = match.ingress.map(name =>
      name.startsWith('aep-model-gateway-') ? 'aep-model-gateway' : name.startsWith('aep-anthropic-') ? 'aep-anthropic-bench-anthropic' : name);
    await writeFile(path.join(doc.kind === 'Ingress' ? ingressDir : pluginDir, doc.metadata.name + '.yaml'), JSON.stringify(doc));
  }
  if (running) {
    // Windows host bind mounts do not reliably notify the standalone watcher.
    await command('docker', ['restart', project + '-higress-1']);
    await ready();
  }
}

async function inference(user, protocol, headers = {}) {
  const anthropic = protocol.startsWith('anthropic');
  const stream = protocol.endsWith('sse');
  const response = await fetch(gateway + (anthropic ? '/bench-anthropic/v1/messages' : '/v1/chat/completions'), {
    method: 'POST', signal: AbortSignal.timeout(20_000),
    headers: {'Content-Type': 'application/json', Authorization: 'Bearer ' + user.token, ...headers},
    body: JSON.stringify({model: anthropic ? 'bench-anthropic' : 'enterprise-chat', max_tokens: 64, stream,
      ...(!anthropic && stream ? {stream_options: {include_usage: true}} : {}), messages: [{role: 'user', content: 'native plugin regression'}]}),
  });
  return {status: response.status, text: await response.text()};
}

try {
  await Promise.all([mkdir(pluginDir), mkdir(ingressDir)]);
  await render();
  const bridge = yaml.load(await readFile(path.join(root, 'deploy/compose/higress/mcpbridge.yaml'), 'utf8'));
  bridge.spec.registries.push({name: 'redis', type: 'dns', domain: 'redis.aep.internal', port: 6379, protocol: 'http'});
  await writeFile(path.join(work, 'mcpbridge.json'), JSON.stringify(bridge));
  // Referencing the discovered Redis service makes its outbound cluster visible
  // to the plugin's native Redis host calls in standalone mode.
  await writeFile(path.join(ingressDir, 'native-redis.yaml'), JSON.stringify({apiVersion: 'networking.k8s.io/v1', kind: 'Ingress',
    metadata: {name: 'native-redis', namespace: 'higress-system', annotations: {'higress.io/destination': 'redis.dns'}},
    spec: {ingressClassName: 'higress', rules: [{http: {paths: [{path: '/native-redis-unused', pathType: 'Prefix',
      backend: {service: {name: 'redis', port: {number: 6379}}}}]}}]}}));
  await writeFile(files[2], JSON.stringify({services: {
    redis: {image: 'redis:7-alpine', command: ['redis-server', '--save', '', '--appendonly', 'no'],
      networks: {default: {aliases: ['redis.aep.internal']}},
      healthcheck: {test: ['CMD', 'redis-cli', 'ping'], interval: '2s', timeout: '2s', retries: 30}},
    'control-service': {environment: {AEP_GATEWAY_QUOTA_URL: 'http://higress:8080' + quotaPath, AEP_GATEWAY_QUOTA_TOKEN: 'disposable-native-e2e-admin'}},
    'gateway-authorizer': {environment: {AEP_GATEWAY_IDENTITY_URL: 'http://control-service:8080/internal/gateway/identity'}},
    higress: {depends_on: {redis: {condition: 'service_healthy'}}, volumes: [
      pluginDir.replaceAll('\\', '/') + ':/data/wasmplugins', ingressDir.replaceAll('\\', '/') + ':/data/ingresses',
      path.join(work, 'mcpbridge.json').replaceAll('\\', '/') + ':/data/mcpbridges/default.yaml:ro']},
  }}));
  await compose('up', '-d', '--build');
  running = true;
  await ready();
  const admin = new AepClient({baseUrl: control, tokenStore: new MemoryTokenStore()});
  await admin.loginWithPassword({deploymentId: 'demo', username: 'admin', password: 'change-this-admin-password'});
  const credential = await admin.createCredential({name: 'Native mock', service: 'mock-openai', type: 'api_key', deliveryMode: 'server_only', value: 'm1-e2e-provider-secret', enabled: true});
  await admin.createModel({displayName: 'Enterprise Chat', sourceType: 'gateway', protocol: 'openai-compatible', endpoint: gateway + '/v1', upstreamModel: 'mock-upstream-chat', credentialId: credential.id, capabilities: ['text'], isDefault: true, enabled: true});
  await admin.createModel({displayName: 'Bench Anthropic', sourceType: 'gateway', protocol: 'anthropic', endpoint: 'http://mock-openai.aep.internal:8080/api/anthropic', upstreamModel: 'mock-upstream-chat', credentialId: credential.id, capabilities: ['text'], isDefault: false, enabled: true});
  const users = [];
  for (let i = 0; i < 2; i++) {
    const username = 'native-user-' + i;
    const user = await admin.createUser({deploymentId: 'demo', username, displayName: username, temporaryPassword: 'native-disposable-password', requirePasswordChange: false, teamIds: ['all-users'], roleIds: ['admin']});
    for (const modelId of ['enterprise-chat', 'bench-anthropic']) await admin.createModelAssignment({modelId, subject: {type: 'user', id: user.id}});
    const store = new MemoryTokenStore();
    const client = new AepClient({baseUrl: control, tokenStore: store});
    await client.loginWithPassword({deploymentId: 'demo', username, password: 'native-disposable-password'});
    users.push({...user, token: (await store.get()).modelAccessToken});
    await admin.refreshGatewayQuota(user.id, 1000);
  }
  const protocols = ['openai', 'openai-sse', 'anthropic', 'anthropic-sse'];
  for (const protocol of protocols) {
    await admin.refreshGatewayQuota(users[0].id, 1000);
    const response = await inference(users[0], protocol);
    assert(response.status === 200, protocol + ' call failed: ' + response.status + ' ' + response.text);
    if (protocol.endsWith('sse')) assert(response.text.includes(protocol.startsWith('anthropic') ? 'event: message_stop' : '[DONE]'), 'Missing terminal stream event');
    await until(async () => assert((await admin.getGatewayQuota(users[0].id)).quota === 997, protocol + ' quota did not deduct exactly the provider usage'), 15_000);
    assert((await admin.getGatewayQuota(users[1].id)).quota === 1000, 'Another user was charged');
    await admin.refreshGatewayQuota(users[0].id, 0);
    assert((await inference(users[0], protocol, {'X-Mse-Consumer': 'aep-quota-' + suffix})).status === 403, protocol + ' zero quota was bypassed');
    await admin.changeGatewayQuota(users[0].id, 1000);
    assert((await inference(users[0], protocol)).status === 200, protocol + ' native delta did not restore access');
    console.log('PASS native quota deduction, user isolation, exhaustion and delta: ' + protocol);
  }
  for (const scopeType of ['global', 'team']) {
    rules = [{id: 'native-token-regression', version: 1, configuration: {kind: 'tokens', scopeType,
      ...(scopeType === 'team' ? {scopeId: 'all-users'} : {}), maximum: 1, interval: 'minute', enabled: true, expectedVersion: 0}}];
    await render();
    for (const protocol of protocols) {
      // Only this test's disposable Redis DB is reset between protocol cases.
      await redis('FLUSHDB');
      for (const user of users) await admin.refreshGatewayQuota(user.id, 1000);
      const first = await inference(users[0], protocol);
      assert(first.status === 200, scopeType + ' first ' + protocol + ' call failed: ' + first.status + ' ' + first.text);
      await until(async () => {
        const keys = (await redis('--scan', '--pattern', 'higress-token-ratelimit*')).trim().split(/\r?\n/).filter(Boolean);
        assert(keys.length === 1, 'Native Token counter is missing');
        assert((await redis('GET', keys[0])).trim() === '3', 'Native Token counter differs from the mock provider usage');
      }, 15_000);
      assert((await inference(users[1], protocol)).status === 429, scopeType + ' did not share the native Token limit across users for ' + protocol);
      console.log('PASS native shared Token limit: ' + scopeType + '/' + protocol);
    }
    rules[0].configuration.enabled = false;
    await render();
    assert((await inference(users[1], 'openai')).status === 200, 'Disabled Token rule still rejects calls');
  }
  await command('docker', ['stop', project + '-redis-1']);
  assert((await inference(users[0], 'openai')).status >= 400, 'Redis outage allowed inference');
  let unavailable = false;
  try { await admin.getGatewayQuota(users[0].id); } catch (error) { unavailable = error.status === 503; }
  assert(unavailable, 'Quota API did not report the Redis outage');
  console.log('PASS native Redis outage fails closed');
  console.log('AEP native quota and Token enforcement regression passed.');
} catch (error) {
  console.error(await compose('logs', '--no-color', '--tail=80', 'higress', 'redis'));
  console.error(await command('docker', ['exec', project + '-higress-1', 'tail', '-n', '100', '/var/log/higress/gateway.log'], {allowFailure: true}));
  throw error;
} finally {
  await compose('down', '-v', '--remove-orphans');
}

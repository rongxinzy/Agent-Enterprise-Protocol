// Explicit, bounded manual verification. The key arrives only on stdin in the
// parent runner and never enters process.env, build inputs, logs or a report.
import assert from 'node:assert/strict';
import {randomBytes} from 'node:crypto';

export async function readRuntimeProviderKey() {
  if (process.env.AEP_NATIVE_ACCESS_REAL_PROVIDER !== 'deepseek') return undefined;
  assert.equal(process.env.AEP_NATIVE_ACCESS_MANAGED, 'true');
  console.log('Waiting for runtime provider credential on stdin (not echoed)');
  if (process.stdin.isTTY) process.stdin.setRawMode(true);
  let input = '';
  try {
    await new Promise((resolve, reject) => {
      const data = chunk => {
        input += chunk;
        if (/[\r\n]/.test(input)) {
          process.stdin.off('data', data);
          process.stdin.pause();
          resolve();
        }
      };
      process.stdin.on('data', data);
      process.stdin.once('end', resolve);
      process.stdin.once('error', reject);
    });
  } finally {
    if (process.stdin.isTTY) process.stdin.setRawMode(false);
  }
  const key = input.trim();
  assert(key.startsWith('sk-') && key.length < 512, 'Invalid runtime provider credential');
  return key;
}

export async function realProviderAcceptance(ctx) {
  const {admin, gateway, fleet, person, roleId, realProviderKey, apply, kubectl, until, record, report} = ctx;
  assert.equal(process.env.AEP_NATIVE_ACCESS_REAL_PROVIDER, 'deepseek');
  assert(realProviderKey && gateway.nativeRedis, 'Real provider requires explicit runtime key and verified native Redis');
  let credential, model, assignment, rule;
  const {redis, publication, loaded, rulePlugin} = gateway.nativeRedis;
  report.realProvider = {provider: 'deepseek', model: 'deepseek-flash', completed: false,
    phase: 'preparing', inferenceAttempted: false};
  try {
    credential = await admin.createCredential({name: 'Disposable cloud acceptance', service: 'deepseek', type: 'api_key',
      deliveryMode: 'server_only', value: realProviderKey, enabled: true});
    // This scenario tests native identity, inference and usage enforcement.
    // Credential delivery is deliberately test-supplied, not claimed as publisher evidence.
    await apply([{apiVersion: 'v1', kind: 'Secret', metadata: {name: 'aep-credential-' + credential.id, namespace: 'aep-credentials'}, stringData: {'api-key': realProviderKey}}]);
    model = await admin.createModel({displayName: 'Disposable DeepSeek Flash', sourceType: 'gateway', protocol: 'openai-compatible',
      endpoint: 'https://api.deepseek.com/v1', credentialId: credential.id, upstreamModel: 'deepseek-flash',
      capabilities: ['text'], isDefault: false, enabled: true});
    assignment = await admin.createModelAssignment({modelId: model.id, subject: {type: 'role', id: roleId}});
    person.tokens = await person.client.refreshSession();
    await until(async () => {
      const grant = JSON.parse(await kubectl(['-n', 'aep-system', 'get', 'modelaccessgrant', 'access-' + person.binding, '-o', 'json']));
      assert(grant.spec.models.includes(model.id));
    });
    rule = await admin.putGatewayLimit('cloud-' + randomBytes(5).toString('hex'), {kind: 'tokens', scopeType: 'user', scopeId: person.user.id,
      modelId: model.id, maximum: 1, interval: 'minute', enabled: true, expectedVersion: 0});
    report.realProvider.phase = 'publishing';
    await publication(admin);
    const name = await until(() => rulePlugin(rule));
    await until(() => loaded(name, model.id));
    // Check every discovered verifier directly, without invoking a provider.
    await until(() => fleet.check(person, model.id, 200));
    await redis('FLUSHDB');
    const body = JSON.stringify({model: model.id, messages: [{role: 'user', content: 'Reply with OK only.'}],
      max_tokens: 16, thinking: {type: 'disabled'}, stream: false});
    // Exactly one billable attempt; never retry a provider request implicitly.
    report.realProvider.phase = 'inference';
    report.realProvider.inferenceAttempted = true;
    const result = await gateway.call(person, model.id, {body, timeout: 45000});
    report.realProvider.responseStatus = result.status;
    assert.equal(result.status, 200, 'DeepSeek Flash request failed (response content suppressed)');
    assert(result.body.choices?.[0]?.message?.content?.length > 0, 'DeepSeek did not return assistant content');
    const usage = result.body.usage;
    assert(Number.isSafeInteger(usage?.total_tokens) && usage.total_tokens > 1, 'Provider usage is missing');
    await until(async () => {
      const keys = (await redis('--scan', '--pattern', 'higress-token-ratelimit*')).split(/\r?\n/).filter(Boolean);
      assert.equal(keys.length, 1);
      assert.equal(await redis('GET', keys[0]), String(usage.total_tokens), 'Native Redis Token count differs from DeepSeek usage');
    });
    const denied = await gateway.call(person, model.id, {body, timeout: 10000, headers: {'x-mse-consumer': 'quota-admin', 'x-aep-user-id': 'other'}});
    assert.equal(denied.status, 429, 'Real-provider Token limit could be bypassed');
    report.realProvider = {...report.realProvider, completed: true, phase: 'verified', usage, tokenLimitDenied: true,
      credentialDelivery: 'test-supplied Secret; not credential-publisher acceptance'};
    record('Real DeepSeek Flash responds through managed ingress; native Redis count equals provider usage and rejects the next request');
  } finally {
    if (rule) await admin.deleteGatewayLimit(rule.id, rule.version);
    if (assignment) await admin.deleteModelAssignment(assignment.id);
    if (model) await admin.deleteModel(model.id);
    await publication(admin);
    if (credential) {
      await admin.deleteCredential(credential.id);
      await kubectl(['-n', 'aep-credentials', 'delete', 'secret', 'aep-credential-' + credential.id, '--ignore-not-found']);
    }
  }
}

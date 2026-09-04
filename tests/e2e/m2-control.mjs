import {spawn} from 'node:child_process';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

import {AepClient, MemoryTokenStore} from '../../packages/aep-sdk-node/dist/index.js';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const composeFile = path.join(root, 'deploy', 'compose', 'compose.yaml');
const project = 'aep-m2-control-e2e';
const port = process.env.AEP_M2_CONTROL_E2E_PORT ?? '18085';
const baseUrl = 'http://localhost:' + port;
const composeEnv = {
  AEP_PORT: port,
  AEP_MINIO_CONSOLE_PORT: process.env.AEP_M2_CONTROL_MINIO_CONSOLE_PORT ?? '19005',
};
const runId = Date.now().toString(36);

try {
  await compose('up', '-d', '--build');
  await waitForHealth();
  await runScenario();
  console.log('AEP M2 credential control scenario passed.');
} catch (error) {
  await compose('logs', '--no-color', '--tail=200', 'control-service', 'postgres', true);
  throw error;
} finally {
  await compose('down', '-v', '--remove-orphans', true);
}

async function runScenario() {
  const adminStore = new MemoryTokenStore();
  const admin = new AepClient({
    baseUrl, tokenStore: adminStore,
  });
  await admin.loginWithPassword({deploymentId: 'demo', username: 'admin', password: 'change-this-admin-password'});
  const metadata = await admin.getMetadata();
  assert(metadata.capabilities.includes('credentials'), 'Configured Credential capability was not advertised');

  const roleId = 'm2-role-' + runId;
  const teamId = 'm2-team-' + runId;
  await adminRequest('/aep/v1/admin/roles', {
    id: roleId,
    name: 'M2 Role',
    description: 'M2 credential control test role',
    permissions: ['credentials.read'],
  }, adminStore);
  await adminRequest('/aep/v1/admin/teams', {
    id: teamId,
    name: 'M2 Team',
    description: 'M2 credential control test team',
  }, adminStore);

  const username = 'm2-user-' + runId;
  const password = 'temporary-password-123';
  const user = await admin.createUser({
    deploymentId: 'demo', username, displayName: 'M2 User ' + runId,
    temporaryPassword: password, requirePasswordChange: false,
    teamIds: [teamId], roleIds: [roleId],
  });
  const users = await admin.listUsers();
  const listedUser = users.items.find(item => item.id === user.id);
  assert(listedUser?.roleIds?.includes(roleId), 'User list omitted the assigned Role');
  assert(listedUser?.teamIds?.includes(teamId), 'User list omitted the assigned Team');
  const roles = await adminGet('/aep/v1/admin/roles', adminStore);
  const listedRole = roles.roles.find(item => item.id === roleId);
  assert(listedRole?.permissions?.includes('credentials.read'), 'Role list omitted the assigned permission');
  const teams = await adminGet('/aep/v1/admin/teams', adminStore);
  const listedTeam = teams.teams.find(item => item.id === teamId);
  assert(listedTeam?.memberCount === 1, 'Team list returned the wrong member count');
  const userStore = new MemoryTokenStore();
  const userClient = new AepClient({baseUrl, tokenStore: userStore});
  await userClient.loginWithPassword({deploymentId: 'demo', username, password});

  const subjects = [
    {type: 'user', id: user.id},
    {type: 'team', id: teamId},
    {type: 'role', id: roleId},
  ];
  const assigned = [];
  for (const [index, subject] of subjects.entries()) {
    const secret = 'm2-e2e-secret-' + index + '-' + runId;
    const item = await admin.createCredential({
      name: 'M2 ' + subject.type, service: 'service-' + subject.type,
      type: 'api_key', deliveryMode: 'client', value: secret, enabled: true,
    });
    assert(!JSON.stringify(item).includes(secret), 'Create response leaked a secret');
    const assignment = await admin.createCredentialAssignment({credentialId: item.id, subject});
    assigned.push({item, assignment, secret});
  }

  const serverOnlySecret = 'm2-server-only-' + runId;
  const serverOnly = await admin.createCredential({
    name: 'M2 server only', service: 'mock-openai', type: 'api_key',
    deliveryMode: 'server_only', value: serverOnlySecret, enabled: true,
  });
  await admin.createCredentialAssignment({credentialId: serverOnly.id, subject: {type: 'user', id: user.id}});
  const disabled = await admin.createCredential({
    name: 'M2 disabled', service: 'disabled-service', type: 'api_key',
    deliveryMode: 'client', value: 'm2-disabled-' + runId, enabled: false,
  });
  await admin.createCredentialAssignment({credentialId: disabled.id, subject: {type: 'user', id: user.id}});
  const unassigned = await admin.createCredential({
    name: 'M2 unassigned', service: 'unassigned-service', type: 'api_key',
    deliveryMode: 'client', value: 'm2-unassigned-' + runId, enabled: true,
  });

  const visible = (await userClient.listCredentialsForUser()).credentials;
  assert(visible.length === 3, 'RBAC authorization union did not expose exactly three client credentials');
  assert(visible.every(item => item.deliveryMode === 'client' && item.enabled), 'Client list exposed an unavailable credential');
  for (const entry of assigned) {
    const resolved = await userClient.resolveCredentialForUser(entry.item.id, 'M2 integration test');
    assert(resolved.value === entry.secret && resolved.expiresAt === null, 'Resolved Credential value was incorrect');
  }

  const rawTokens = await userStore.get();
  const rawResolve = await fetch(baseUrl + '/aep/v1/user/credentials/' + encodeURIComponent(assigned[0].item.id) + '/resolve', {
    method: 'POST',
    headers: {
      Authorization: 'Bearer ' + rawTokens.accessToken,
      'Content-Type': 'application/json',
      'X-AEP-Protocol-Version': '1.0',
    },
    body: JSON.stringify({purpose: 'verify no-store response'}),
  });
  assert(rawResolve.status === 200 && rawResolve.headers.get('cache-control') === 'no-store', 'Resolve response was cacheable');
  assert((await rawResolve.json()).value === assigned[0].secret, 'Raw resolve returned the wrong value');

  await expectProblem(userClient.resolveCredentialForUser(serverOnly.id, 'must stay server side'), 403, 'CREDENTIAL_SERVER_ONLY');
  await expectProblem(userClient.resolveCredentialForUser(disabled.id, 'must be enabled'), 403, 'CREDENTIAL_DISABLED');
  await expectProblem(userClient.resolveCredentialForUser(unassigned.id, 'not assigned'), 403, 'ACCESS_DENIED');
  await expectProblem(
    admin.createCredentialAssignment({credentialId: assigned[0].item.id, subject: subjects[0]}),
    409,
    'ASSIGNMENT_EXISTS',
  );

  const rotatedSecret = 'm2-rotated-secret-' + runId;
  const rotated = await admin.rotateCredential(assigned[0].item.id, {value: rotatedSecret});
  assert(!JSON.stringify(rotated).includes(rotatedSecret), 'Rotate response leaked the new secret');
  assert((await userClient.resolveCredentialForUser(assigned[0].item.id, 'after rotation')).value === rotatedSecret, 'Rotation did not replace the resolved value');

  await admin.updateCredential(assigned[1].item.id, {deliveryMode: 'server_only'});
  assert((await userClient.listCredentialsForUser()).credentials.length === 2, 'Delivery-mode change did not update client discovery');
  await expectProblem(userClient.resolveCredentialForUser(assigned[1].item.id, 'after restriction'), 403, 'CREDENTIAL_SERVER_ONLY');
  await admin.updateCredential(assigned[1].item.id, {deliveryMode: 'client'});

  await expectProblem(admin.createModel({
    id: 'missing-credential-model', displayName: 'Invalid model', sourceType: 'gateway',
    protocol: 'openai-compatible', endpoint: 'https://models.example.test/v1',
    upstreamModel: 'invalid', credentialId: 'missing-' + runId,
    capabilities: ['text'], contextWindow: 4096, isDefault: false, enabled: true,
  }), 404, 'RESOURCE_NOT_FOUND');
  await admin.createModel({
    id: 'm2-model-' + runId, displayName: 'M2 model', sourceType: 'gateway',
    protocol: 'openai-compatible', endpoint: 'https://models.example.test/v1',
    upstreamModel: 'mock-upstream', credentialId: serverOnly.id,
    capabilities: ['text'], contextWindow: 4096, isDefault: false, enabled: true,
  });
  await expectProblem(admin.deleteCredential(serverOnly.id), 409, 'CREDENTIAL_IN_USE');

  const adminList = await admin.listCredentials();
  const serializedAdminList = JSON.stringify(adminList);
  for (const forbidden of [...assigned.map(item => item.secret), rotatedSecret, serverOnlySecret]) {
    assert(!serializedAdminList.includes(forbidden), 'Administrator metadata leaked a secret');
  }
  const cliList = await commandOutput('go', [
    'run', './cmd/aepctl', '--base-url', baseUrl, '--deployment', 'demo',
    '--username', 'admin', '--password', 'change-this-admin-password',
    'credential', 'list',
  ]);
  assert(JSON.parse(cliList).credentials.length === 6, 'aepctl credential list returned an unexpected catalog');
  assert(!cliList.includes(rotatedSecret), 'aepctl output leaked a secret');
  const cliBaseArgs = [
    'run', './cmd/aepctl', '--base-url', baseUrl, '--deployment', 'demo',
    '--username', 'admin', '--password', 'change-this-admin-password',
    'credential',
  ];
  const cliSecret = 'm2-cli-secret-' + runId;
  const cliCredentialOutput = await commandOutput('go', [
    ...cliBaseArgs, 'create', '--name', 'CLI Credential', '--service', 'cli-service', '--delivery-mode', 'client',
  ], {AEPCTL_CREDENTIAL_VALUE: cliSecret});
  const cliCredential = JSON.parse(cliCredentialOutput);
  assert(cliCredential.id && !cliCredentialOutput.includes(cliSecret), 'aepctl credential create leaked or omitted data');
  const cliAssignment = JSON.parse(await commandOutput('go', [
    ...cliBaseArgs, 'assign', '--credential-id', cliCredential.id, '--subject-type', 'user', '--subject-id', user.id,
  ]));
  const cliRotatedSecret = 'm2-cli-rotated-' + runId;
  const cliRotateOutput = await commandOutput('go', [
    ...cliBaseArgs, 'rotate', '--credential-id', cliCredential.id,
  ], {AEPCTL_CREDENTIAL_VALUE: cliRotatedSecret});
  assert(!cliRotateOutput.includes(cliRotatedSecret), 'aepctl credential rotate leaked the replacement secret');
  await commandOutput('go', [...cliBaseArgs, 'update', '--credential-id', cliCredential.id, '--enabled=false']);
  assert((await commandOutput('go', [...cliBaseArgs, 'assignments'])).includes(cliAssignment.id), 'aepctl did not list its Credential assignment');
  await commandOutput('go', [...cliBaseArgs, 'revoke', '--assignment-id', cliAssignment.id]);
  await commandOutput('go', [...cliBaseArgs, 'delete', '--credential-id', cliCredential.id]);

  const encryptedRows = await postgres("SELECT id||':'||key_id||':'||encode(nonce,'hex')||':'||encode(encrypted_value,'base64') FROM credentials ORDER BY id");
  assert(encryptedRows.includes('sha256:'), 'Credential rows did not record an encryption key identifier');
  for (const forbidden of [rotatedSecret, serverOnlySecret]) {
    assert(!encryptedRows.includes(forbidden), 'PostgreSQL stored plaintext Credential material');
  }

  await compose('restart', 'control-service');
  await waitForHealth();
  assert((await userClient.resolveCredentialForUser(assigned[0].item.id, 'after service restart')).value === rotatedSecret, 'Credential could not be decrypted after service restart');

  await admin.deleteCredentialAssignment(assigned[2].assignment.id);
  await expectProblem(userClient.resolveCredentialForUser(assigned[2].item.id, 'after revocation'), 403, 'ACCESS_DENIED');
  const audit = await postgres("SELECT outcome||':'||count(*) FROM credential_resolution_audit GROUP BY outcome ORDER BY outcome");
  assert(audit.includes('resolved:') && audit.includes('denied:'), 'Credential resolution audit did not retain both outcomes');

  await admin.deleteModel('m2-model-' + runId);
  await admin.deleteCredential(serverOnly.id);
  await expectProblem(admin.getCredential(serverOnly.id), 404, 'RESOURCE_NOT_FOUND');
}

async function adminRequest(path, body, tokenStore) {
  const tokens = await tokenStore.get();
  assert(tokens?.accessToken, 'Admin session did not provide an access token');
  const response = await fetch(baseUrl + path, {
    method: 'POST',
    headers: {
      Authorization: 'Bearer ' + tokens.accessToken,
      'Content-Type': 'application/json',
      'X-AEP-Protocol-Version': '1.0',
    },
    body: JSON.stringify(body),
  });
  const text = await response.text();
  if (!response.ok) throw new Error('Admin request ' + path + ' failed with ' + response.status + ': ' + text);
  return text ? JSON.parse(text) : null;
}

async function adminGet(path, tokenStore) {
  const tokens = await tokenStore.get();
  assert(tokens?.accessToken, 'Admin session did not provide an access token');
  const response = await fetch(baseUrl + path, {
    headers: {
      Authorization: 'Bearer ' + tokens.accessToken,
      'X-AEP-Protocol-Version': '1.0',
    },
  });
  const text = await response.text();
  if (!response.ok) throw new Error('Admin request ' + path + ' failed with ' + response.status + ': ' + text);
  return text ? JSON.parse(text) : null;
}

async function expectProblem(promise, status, code) {
  try {
    await promise;
  } catch (error) {
    assert(error.status === status && error.code === code, 'Expected ' + status + ' ' + code + ', received ' + error.status + ' ' + error.code);
    return;
  }
  throw new Error('Expected ' + status + ' ' + code);
}

async function waitForHealth() {
  const deadline = Date.now() + 180_000;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(baseUrl + '/healthz');
      if (response.ok) return;
    } catch {}
    await new Promise(resolve => setTimeout(resolve, 1_000));
  }
  throw new Error('Control service did not become healthy within 180 seconds');
}

function postgres(query) {
  return commandOutput('docker', [
    'compose', '-p', project, '-f', composeFile, 'exec', '-T', 'postgres',
    'psql', '-U', 'aep', '-d', 'aep', '-Atc', query,
  ], composeEnv);
}

function compose(...args) {
  let allowFailure = false;
  if (args.at(-1) === true) {
    allowFailure = true;
    args.pop();
  }
  return command('docker', ['compose', '-p', project, '-f', composeFile, ...args], composeEnv, allowFailure);
}

function commandOutput(executable, args, extraEnv = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {cwd: root, env: {...process.env, ...extraEnv}, stdio: ['ignore', 'pipe', 'pipe'], shell: false});
    const stdout = [];
    const stderr = [];
    child.stdout.on('data', chunk => stdout.push(Buffer.from(chunk)));
    child.stderr.on('data', chunk => stderr.push(Buffer.from(chunk)));
    child.on('error', reject);
    child.on('exit', code => {
      const output = Buffer.concat(stdout).toString('utf8').trim();
      const errors = Buffer.concat(stderr).toString('utf8').trim();
      if (code === 0) resolve(output);
      else reject(new Error(executable + ' ' + args.join(' ') + ' exited with ' + code + ': ' + errors));
    });
  });
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

import {spawn} from 'node:child_process';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

import {AepClient, MemoryTokenStore} from '../../packages/aep-sdk-node/dist/index.js';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const composeFiles = [
  path.join(root, 'deploy', 'compose', 'compose.yaml'),
  path.join(root, 'tests', 'e2e', 'agent-split.compose.yaml'),
];
const project = 'aep-agent-split-e2e';
const controlPort = process.env.AEP_AGENT_SPLIT_CONTROL_PORT ?? '18087';
const agentPort = process.env.AEP_AGENT_SPLIT_AGENT_PORT ?? '18088';
const composeEnv = {AEP_PORT: controlPort, AEP_AGENT_CONTROL_PORT: agentPort, AEP_MINIO_CONSOLE_PORT: process.env.AEP_AGENT_SPLIT_MINIO_CONSOLE_PORT ?? '19009'};

const controlUrl = `http://localhost:${controlPort}`;
const agentUrl = `http://localhost:${agentPort}`;

// Minimal fetch shim so the recording transport above can forward real
// traffic (the SDK's FetchTransport is not exported with per-call bases).
class FetchTransportShim {
  constructor(controlUrl, agentUrl) {
    this.bases = new Set([controlUrl, agentUrl]);
  }
  async request(baseUrl, request) {
    assert(this.bases.has(baseUrl), `unexpected base ${baseUrl}`);
    const response = await fetch(baseUrl + request.path, {
      method: request.method,
      headers: {...request.headers, 'X-AEP-Protocol-Version': '1.0'},
      body: request.body === undefined ? undefined : JSON.stringify(request.body),
    });
    const text = await response.text();
    return {status: response.status, data: text ? JSON.parse(text) : {}, headers: response.headers};
  }
}

async function waitForHealth(url, timeout = 180_000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url);
      if (response.ok) return;
    } catch {}
    await new Promise(resolve => setTimeout(resolve, 1_000));
  }
  throw new Error(`${url} did not become healthy within ${timeout}ms`);
}

try {
  await compose('up', '-d', '--build', '--wait');
  await waitForHealth(`${controlUrl}/healthz`);
  await waitForHealth(`${agentUrl}/healthz`);

  // Surface separation: the agent process serves metadata, auth, and the
  // user runtime — never the admin API. The enterprise process serves all
  // of them (all-in-one compatibility).
  const agentMetadata = await (await fetch(`${agentUrl}/aep/v1/metadata`, {headers: {'X-AEP-Protocol-Version': '1.0'}})).json();
  assert(agentMetadata.service === 'aep-control-service', 'agent service metadata missing');
  const adminOnAgent = await fetch(`${agentUrl}/aep/v1/admin/models`, {headers: {'X-AEP-Protocol-Version': '1.0'}});
  assert(adminOnAgent.status === 404, `admin API leaked onto the agent surface: ${adminOnAgent.status}`);
  const controlMetadata = await (await fetch(`${controlUrl}/aep/v1/metadata`, {headers: {'X-AEP-Protocol-Version': '1.0'}})).json();
  assert(controlMetadata.agentControl?.baseUrl === `http://localhost:${agentPort}`, `metadata did not advertise the agent endpoint: ${JSON.stringify(controlMetadata.agentControl)}`);
  const userOnControl = await fetch(`${controlUrl}/aep/v1/user/me`, {headers: {'X-AEP-Protocol-Version': '1.0'}});
  assert(userOnControl.status === 401, `all-in-one surface rejected unauthenticated user call with ${userOnControl.status}`);

  // Shared session plane: tokens issued by one process are valid on the
  // other (same signing key, same session store).
  const bootstrap = new AepClient({baseUrl: controlUrl, tokenStore: new MemoryTokenStore()});
  await bootstrap.loginWithPassword({deploymentId: 'demo', username: 'admin', password: 'change-this-admin-password'});
  const controlIdentity = await bootstrap.getCurrentIdentity();
  assert(controlIdentity.roles.includes('admin'), 'enterprise login identity lacks the admin role');

  const directAgentClient = new AepClient({baseUrl: agentUrl, tokenStore: new MemoryTokenStore()});
  await directAgentClient.loginWithPassword({deploymentId: 'demo', username: 'admin', password: 'change-this-admin-password'});
  const heartbeat = await directAgentClient.heartbeatUser({status: 'online'});
  assert(heartbeat.nextHeartbeatAfterSeconds > 0, 'agent heartbeat failed');

  // Split routing in the SDK: one client, two bases — auth and the user
  // runtime travel to the agent process, admin stays on the enterprise API.
  const seen = [];
  const split = new AepClient({
    baseUrl: controlUrl,
    agentControlBaseUrl: agentUrl,
    tokenStore: new MemoryTokenStore(),
    transport: {
      async request(base, request) {
        seen.push({base, path: request.path});
        return new FetchTransportShim(controlUrl, agentUrl).request(base, request);
      },
    },
  });
  await split.loginWithPassword({deploymentId: 'demo', username: 'admin', password: 'change-this-admin-password'});
  await split.listRoles({}).catch(() => undefined);
  await split.heartbeatUser({status: 'online'});
  const loginBase = seen.find(entry => entry.path === '/aep/v1/auth/password/login')?.base;
  const rolesBase = seen.find(entry => entry.path.startsWith('/aep/v1/admin/roles'))?.base;
  const heartbeatBase = seen.find(entry => entry.path === '/aep/v1/user/heartbeat')?.base;
  assert(loginBase === agentUrl, `login routed to ${loginBase}`);
  assert(heartbeatBase === agentUrl, `heartbeat routed to ${heartbeatBase}`);
  assert(rolesBase === controlUrl, `admin routed to ${rolesBase}`);

  console.log('AEP agent-split scenario passed.');
} catch (error) {
  await compose('logs', '--no-color', '--tail=200', 'control-service', 'agent-control', true);
  throw error;
} finally {
  await compose('down', '-v', '--remove-orphans', true);
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function compose(...args) {
  let allowFailure = false;
  if (args.at(-1) === true) {
    allowFailure = true;
    args.pop();
  }
  const composeArgs = ['compose', '-p', project, '--profile', 'agent-split'];
  for (const file of composeFiles) composeArgs.push('-f', file);
  composeArgs.push(...args);
  return command('docker', composeArgs, composeEnv, allowFailure);
}

function command(executable, args, extraEnv = {}, allowFailure = false) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {cwd: root, env: {...process.env, ...extraEnv}, stdio: 'inherit', shell: false});
    child.on('error', reject);
    child.on('exit', code => (code === 0 || allowFailure ? resolve() : reject(new Error(`${executable} ${args.join(' ')} exited with ${code}`))));
  });
}

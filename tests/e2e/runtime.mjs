import {spawn} from 'node:child_process';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const composeFile = path.join(root, 'deploy', 'compose', 'compose.yaml');
const project = 'aep-runtime-e2e';
const port = process.env.AEP_RUNTIME_E2E_PORT ?? '18087';
const baseUrl = 'http://localhost:' + port;
const composeEnv = {
  AEP_PORT: port,
  AEP_MINIO_CONSOLE_PORT: process.env.AEP_RUNTIME_MINIO_CONSOLE_PORT ?? '19007',
  AEP_ENVIRONMENT: 'test',
  AEP_LOG_FORMAT: 'json',
};
const replicas = [project + '-replica-a', project + '-replica-b'];

try {
  await verifyConcurrentStartup();
  await compose('down', '-v', '--remove-orphans', true);
  await compose('up', '-d', '--build');
  await waitForStatus('/readyz', 200);
  await verifyRuntimeEndpoints();
  await verifyDependencyFailure('minio');
  await verifyDependencyFailure('postgres');
  await verifyGracefulShutdown();
  await verifyJSONLogs();
  console.log('AEP production runtime baseline scenario passed.');
} catch (error) {
  await compose('logs', '--no-color', '--tail=300', true);
  throw error;
} finally {
  await compose('down', '-v', '--remove-orphans', true);
}

async function verifyConcurrentStartup() {
  await compose('up', '-d', '--build', 'postgres', 'minio');
  await waitForCommand(() => composeOutput('exec', '-T', 'postgres', 'pg_isready', '-U', 'aep', '-d', 'aep'));
  await waitForCommand(() => composeOutput(
    'run', '--rm', '--no-deps', 'control-service',
    'healthcheck', 'http://minio:9000/minio/health/ready',
  ));
  await Promise.all(replicas.map(name => composeOutput(
    'run', '--detach', '--no-deps', '--name', name, 'control-service',
  )));
  await waitForCommand(async () => {
    const states = await Promise.all(replicas.map(name => commandOutput(
      'docker', ['inspect', '--format', '{{.State.Status}}:{{.State.Health.Status}}', name],
    )));
    if (!states.every(state => state === 'running:healthy')) {
      throw new Error('replicas are not healthy: ' + states.join(', '));
    }
  });
}

async function verifyRuntimeEndpoints() {
  const live = await fetch(baseUrl + '/livez');
  assert(live.status === 200, 'liveness endpoint was not healthy');
  const legacy = await fetch(baseUrl + '/healthz');
  assert(legacy.status === 200, 'legacy health endpoint did not preserve readiness behavior');
  const metadata = await fetch(baseUrl + '/aep/v1/metadata', {
    headers: {'X-Request-ID': 'runtime-request-1'},
  });
  assert(metadata.status === 200 && metadata.headers.get('x-request-id') === 'runtime-request-1', 'request ID was not preserved');
  const metrics = await (await fetch(baseUrl + '/metrics')).text();
  assert(metrics.includes('aep_control_service_http_requests_total'), 'Prometheus request counter was not exposed');
  assert(metrics.includes('route="/aep/v1/metadata"'), 'Prometheus metric omitted the stable route label');
  assert(!metrics.includes('runtime-request-1'), 'Prometheus metrics contained request identifiers');
}

async function verifyDependencyFailure(service) {
  await compose('stop', service);
  await waitForStatus('/readyz', 503);
  const live = await fetch(baseUrl + '/livez');
  assert(live.status === 200, 'liveness failed while ' + service + ' was unavailable');
  await compose('start', service);
  await waitForStatus('/readyz', 200);
}

async function verifyGracefulShutdown() {
  await compose('stop', '-t', '15', 'control-service');
  const exitCode = await commandOutput('docker', [
    'inspect', '--format', '{{.State.ExitCode}}', project + '-control-service-1',
  ]);
  assert(exitCode === '0', 'control-service did not exit cleanly on SIGTERM: ' + exitCode);
  await compose('start', 'control-service');
  await waitForStatus('/readyz', 200);
}

async function verifyJSONLogs() {
  await fetch(baseUrl + '/aep/v1/metadata', {
    headers: {'X-Request-ID': 'runtime-log-request'},
  });
  const output = await composeOutput('logs', '--no-color', '--no-log-prefix', 'control-service');
  const records = output.split(/\r?\n/).filter(Boolean).map(line => JSON.parse(line));
  assert(records.some(item => item.msg === 'control service listening'), 'structured startup log was missing');
  assert(records.some(item => item.msg === 'http request' && item.request_id === 'runtime-log-request'), 'structured access log was missing');
  assert(records.every(item => item.service === 'aep-control-service' && item.environment === 'test'), 'structured log context was incomplete');
}

async function waitForStatus(route, expected) {
  await waitForCommand(async () => {
    const response = await fetch(baseUrl + route);
    if (response.status !== expected) throw new Error(route + ' returned ' + response.status);
  });
}

async function waitForCommand(operation) {
  const deadline = Date.now() + 120_000;
  let lastError;
  while (Date.now() < deadline) {
    try {
      await operation();
      return;
    } catch (error) {
      lastError = error;
    }
    await new Promise(resolve => setTimeout(resolve, 1_000));
  }
  throw new Error('runtime condition was not met: ' + (lastError?.message ?? 'unknown error'));
}

function composeOutput(...args) {
  return commandOutput('docker', ['compose', '-p', project, '-f', composeFile, ...args], composeEnv);
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
    const child = spawn(executable, args, {
      cwd: root,
      env: {...process.env, ...extraEnv},
      stdio: ['ignore', 'pipe', 'pipe'],
      shell: false,
    });
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
    const child = spawn(executable, args, {
      cwd: root,
      env: {...process.env, ...extraEnv},
      stdio: 'inherit',
      shell: false,
    });
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

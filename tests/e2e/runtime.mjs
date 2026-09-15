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
  AEP_LOG_FORMAT: 'json',
  AEP_RETENTION_CLEANUP_INTERVAL: '1s',
  AEP_OPERATIONAL_RETENTION: '1h',
  AEP_TELEMETRY_RETENTION: '1h',
  AEP_AUDIT_RETENTION: '1h',
  AEP_RETENTION_CLEANUP_BATCH_SIZE: '100',
};
const replicas = [project + '-replica-a', project + '-replica-b'];

try {
  await verifyConcurrentStartup();
  await compose('down', '-v', '--remove-orphans', true);
  await compose('up', '-d', '--build');
  await waitForStatus('/readyz', 200);
  await verifyRuntimeEndpoints();
  await verifyRetentionCleanup();
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
  const metadataBody = await metadata.json();
  assert(
    Array.isArray(metadataBody.capabilities) &&
      !metadataBody.capabilities.includes('federated_auth'),
    'local Compose exposed mock federated authentication',
  );
  const metrics = await (await fetch(baseUrl + '/metrics')).text();
  assert(metrics.includes('aep_control_service_http_requests_total'), 'Prometheus request counter was not exposed');
  assert(metrics.includes('route="/aep/v1/metadata"'), 'Prometheus metric omitted the stable route label');
  assert(!metrics.includes('runtime-request-1'), 'Prometheus metrics contained request identifiers');
}

async function verifyRetentionCleanup() {
  await psql(`
DO $$
DECLARE admin_id text;
BEGIN
  SELECT id INTO admin_id FROM users WHERE deployment_id='demo' AND username='admin';
  INSERT INTO user_sessions(session_id,deployment_id,user_id,topic,created_at,last_seen_at,revoked_at) VALUES
    ('retention-old-session','demo',admin_id,'user.retention-old',now()-interval '3 hours',now()-interval '2 hours',now()-interval '2 hours'),
    ('retention-new-session','demo',admin_id,'user.retention-new',now(),now(),NULL),
    ('retention-recently-revoked-session','demo',admin_id,'user.retention-revoked',now()-interval '3 hours',now()-interval '2 hours',now());
  INSERT INTO user_session_tokens(token_hash,session_id,expires_at,revoked_at,created_at) VALUES
    ('retention-old-token','retention-old-session',now()-interval '2 hours',now()-interval '2 hours',now()-interval '3 hours'),
    ('retention-new-token','retention-new-session',now()+interval '1 hour',NULL,now()),
    ('retention-recently-revoked-token','retention-recently-revoked-session',now()+interval '1 hour',now(),now()-interval '3 hours');
  INSERT INTO control_events(event_id,deployment_id,type,scope_type,task_type,state,expires_at,created_by,created_at) VALUES
    ('retention-old-event','demo','model.catalog.changed','global','model.reconcile','cancelled',now()-interval '2 hours',admin_id,now()-interval '3 hours'),
    ('retention-new-event','demo','model.catalog.changed','global','model.reconcile','active',now()+interval '1 hour',admin_id,now());
  INSERT INTO session_control_deliveries(delivery_id,event_id,session_id,state,updated_at) VALUES
    ('retention-old-delivery','retention-old-event','retention-old-session','succeeded',now()-interval '2 hours'),
    ('retention-new-delivery','retention-new-event','retention-new-session','pending',now());
  INSERT INTO telemetry_events(event_id,deployment_id,user_id,session_id,type,payload,occurred_at,received_at) VALUES
    ('retention-old-telemetry','demo',admin_id,'retention-old-session','skill.sync.completed','{}',now()-interval '2 hours',now()-interval '2 hours'),
    ('retention-new-telemetry','demo',admin_id,'retention-new-session','skill.sync.completed','{}',now(),now());
  INSERT INTO skill_sync_results(id,deployment_id,user_id,session_id,revision,status,created_at) VALUES
    ('retention-old-sync','demo',admin_id,'retention-old-session','old','succeeded',now()-interval '2 hours'),
    ('retention-new-sync','demo',admin_id,'retention-new-session','new','succeeded',now());
  INSERT INTO authentication_audit_events(deployment_id,user_id,session_id,event_type,outcome,principal_hash,source_hash,created_at) VALUES
    ('demo',admin_id,'retention-old-session','login.succeeded','success','old-principal','old-source',now()-interval '2 hours'),
    ('demo',admin_id,'retention-new-session','login.succeeded','success','new-principal','new-source',now());
  INSERT INTO credential_resolution_audit(id,deployment_id,credential_id,user_id,session_id,purpose,outcome,created_at) VALUES
    ('retention-old-credential-audit','demo','retention-credential',admin_id,'retention-old-session','runtime-e2e','resolved',now()-interval '2 hours'),
    ('retention-new-credential-audit','demo','retention-credential',admin_id,'retention-new-session','runtime-e2e','resolved',now());
  INSERT INTO license_audit_events(id,deployment_id,license_id,actor_user_id,action,outcome,created_at) VALUES
    ('retention-old-license-audit','demo','retention-license',admin_id,'import','success',now()-interval '2 hours'),
    ('retention-new-license-audit','demo','retention-license',admin_id,'import','success',now());
  INSERT INTO login_rate_limits(key_hash,failure_count,updated_at) VALUES
    ('retention-old-limit',1,now()-interval '2 hours'),
    ('retention-new-limit',1,now());
END $$;
  `);

  await waitForCommand(async () => {
    const oldRows = await psql(`SELECT
      (SELECT count(*) FROM user_sessions WHERE session_id='retention-old-session')+
      (SELECT count(*) FROM user_session_tokens WHERE token_hash='retention-old-token')+
      (SELECT count(*) FROM control_events WHERE event_id='retention-old-event')+
      (SELECT count(*) FROM session_control_deliveries WHERE delivery_id='retention-old-delivery')+
      (SELECT count(*) FROM telemetry_events WHERE event_id='retention-old-telemetry')+
      (SELECT count(*) FROM skill_sync_results WHERE id='retention-old-sync')+
      (SELECT count(*) FROM authentication_audit_events WHERE principal_hash='old-principal')+
      (SELECT count(*) FROM credential_resolution_audit WHERE id='retention-old-credential-audit')+
      (SELECT count(*) FROM license_audit_events WHERE id='retention-old-license-audit')+
      (SELECT count(*) FROM login_rate_limits WHERE key_hash='retention-old-limit')`);
    if (oldRows !== '0') throw new Error('expired retention rows remain: ' + oldRows);
  });

  const newRows = await psql(`SELECT
    (SELECT count(*) FROM user_sessions WHERE session_id='retention-new-session')+
    (SELECT count(*) FROM user_sessions WHERE session_id='retention-recently-revoked-session')+
    (SELECT count(*) FROM user_session_tokens WHERE token_hash='retention-new-token')+
    (SELECT count(*) FROM user_session_tokens WHERE token_hash='retention-recently-revoked-token')+
    (SELECT count(*) FROM control_events WHERE event_id='retention-new-event')+
    (SELECT count(*) FROM session_control_deliveries WHERE delivery_id='retention-new-delivery')+
    (SELECT count(*) FROM telemetry_events WHERE event_id='retention-new-telemetry')+
    (SELECT count(*) FROM skill_sync_results WHERE id='retention-new-sync')+
    (SELECT count(*) FROM authentication_audit_events WHERE principal_hash='new-principal')+
    (SELECT count(*) FROM credential_resolution_audit WHERE id='retention-new-credential-audit')+
    (SELECT count(*) FROM license_audit_events WHERE id='retention-new-license-audit')+
    (SELECT count(*) FROM login_rate_limits WHERE key_hash='retention-new-limit')`);
  assert(newRows === '12', 'retention cleanup removed current, active, or recently revoked rows: ' + newRows);

  const indexes = await psql(`SELECT count(*) FROM pg_class WHERE relkind='i' AND relname IN (
    'idx_authentication_audit_retention','idx_credential_resolution_audit_retention',
    'idx_license_audit_retention','idx_telemetry_retention','idx_skill_sync_retention',
    'idx_session_deliveries_retention','idx_control_events_retention',
    'idx_session_tokens_retention','idx_user_sessions_retention')`);
  assert(indexes === '9', 'retention cleanup indexes were not migrated: ' + indexes);
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
  assert(records.every(item => item.service === 'aep-control-service' && item.environment === 'development'), 'structured log context was incomplete');
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

function psql(query) {
  return composeOutput('exec', '-T', 'postgres', 'psql', '-U', 'aep', '-d', 'aep', '-Atc', query);
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

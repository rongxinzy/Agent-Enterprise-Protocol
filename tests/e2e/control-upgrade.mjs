import {readFile, readdir} from 'node:fs/promises';
import {spawn} from 'node:child_process';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const composeFile = path.join(root, 'deploy', 'compose', 'compose.yaml');
const project = 'aep-control-upgrade-e2e';
const port = process.env.AEP_UPGRADE_E2E_PORT ?? '18088';
const baseUrl = 'http://localhost:' + port;
const composeEnv = {
  AEP_PORT: port,
  AEP_MINIO_CONSOLE_PORT: process.env.AEP_UPGRADE_MINIO_CONSOLE_PORT ?? '19008',
  AEP_ENVIRONMENT: 'test',
  AEP_LOG_FORMAT: 'json',
};
const legacyDeployment = 'demo';
const legacyUser = 'legacy-upgrade-user';
const legacyOrganization = 'legacy-upgrade-org';
const legacyAgent = 'legacy-upgrade-agent';
const legacySkill = 'legacy-upgrade-skill';
const legacyModel = 'legacy-upgrade-model';
const legacyLicense = 'legacy-upgrade-license';
const replicas = [project + '-replica-a', project + '-replica-b'];

try {
  await compose('down', '-v', '--remove-orphans', true);
  await compose('up', '-d', 'postgres', 'minio');
  await waitForCommand(() => psql('SELECT 1'));
  await installLegacySchema();
  await seedLegacyData();
  await verifyConcurrentMigration();
  await compose('up', '-d', '--build', 'control-service');
  await waitForStatus('/readyz', 200);
  await verifyMigratedData();
  await verifyApiAfterUpgrade();
  await compose('restart', 'control-service');
  await waitForStatus('/readyz', 200);
  await verifyMigratedData();
  console.log('AEP control-plane upgrade scenario passed.');
} catch (error) {
  await compose('logs', '--no-color', '--tail=300', true);
  throw error;
} finally {
  await compose('down', '-v', '--remove-orphans', true);
  for (const replica of replicas) await command('docker', ['rm', '-f', replica], {}, true);
}

async function installLegacySchema() {
  const files = (await readdir(path.join(root, 'services/control-service/internal/db/migrations')))
    .filter(name => name.endsWith('.sql')).sort();
  const legacyFiles = files.filter(name => Number(name.slice(0, 3)) <= 9);
  assert(legacyFiles.length === 9, 'legacy fixture did not find the expected migration window');
  await psql('CREATE TABLE IF NOT EXISTS schema_migrations (version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())');
  for (const file of legacyFiles) {
    const sql = await readFile(path.join(root, 'services/control-service/internal/db/migrations', file), 'utf8');
    await psqlInput(sql);
    await psql('INSERT INTO schema_migrations (version) VALUES (' + quote(file) + ') ON CONFLICT DO NOTHING');
  }
}

async function seedLegacyData() {
  const sql = `
INSERT INTO enterprises (id, name) VALUES ('demo', 'Legacy Enterprise') ON CONFLICT (id) DO NOTHING;
INSERT INTO deployments (id, name) VALUES ('demo', 'Legacy Enterprise') ON CONFLICT (id) DO NOTHING;
INSERT INTO organizations (id, enterprise_id, name) VALUES ('${legacyOrganization}', 'demo', 'Legacy Organization');
INSERT INTO users (id, enterprise_id, username, display_name, password_hash, require_password_change, organization_ids, role_ids)
VALUES ('${legacyUser}', 'demo', 'legacy-upgrade-user', 'Legacy User', 'legacy-hash', false, ARRAY['${legacyOrganization}'], ARRAY['legacy-role']);
INSERT INTO agents (agent_id, enterprise_id, user_id, agent_version, platform)
VALUES ('${legacyAgent}', 'demo', '${legacyUser}', '0.1.0', 'linux');
INSERT INTO skills (id, name, description) VALUES ('${legacySkill}', 'Legacy Skill', 'Retained during upgrade');
INSERT INTO skill_assignments (id, enterprise_id, skill_id, subject_type, subject_id)
VALUES ('legacy-skill-enterprise', 'demo', '${legacySkill}', 'enterprise', 'demo');
INSERT INTO skill_assignments (id, enterprise_id, skill_id, subject_type, subject_id)
VALUES ('legacy-skill-agent', 'demo', '${legacySkill}', 'agent', '${legacyAgent}');
INSERT INTO models (enterprise_id, id, display_name, source_type, protocol, endpoint, upstream_model, capabilities, context_window)
VALUES ('demo', '${legacyModel}', 'Legacy Model', 'gateway', 'openai-compatible', 'http://gateway.invalid/v1', 'legacy-model', ARRAY['text'], 4096);
INSERT INTO model_assignments (id, enterprise_id, model_id, subject_type, subject_id)
VALUES ('legacy-model-enterprise', 'demo', '${legacyModel}', 'enterprise', 'demo');
INSERT INTO control_events (event_id, enterprise_id, type, scope_type, scope_id, task_type, expires_at, created_by)
VALUES ('legacy-event', 'demo', 'skill.manifest.changed', 'organization', '${legacyOrganization}', 'skill.reconcile', now() + interval '1 day', '${legacyUser}');
INSERT INTO licenses (license_id, enterprise_id, customer_id, deployment_id, digest, key_id, status, issued_at, expires_at, grace_ends_at, user_limit, agent_limit, features, payload)
VALUES ('${legacyLicense}', 'demo', 'legacy-customer', 'demo', 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'legacy-key', 'active', now(), now() + interval '30 days', now() + interval '37 days', 10, 10, ARRAY['enterprise.models'], '{"legacy":true}');
`;
  await psqlInput(sql);
}

async function verifyConcurrentMigration() {
  await compose('build', 'control-service');
  await Promise.all(replicas.map(name => composeOutput('run', '--detach', '--no-deps', '--name', name, 'control-service')));
  await waitForCommand(async () => {
    const states = await Promise.all(replicas.map(name => commandOutput(
      'docker', ['inspect', '--format', '{{.State.Status}}:{{.State.Health.Status}}', name],
    )));
    if (!states.every(state => state === 'running:healthy')) {
      throw new Error('upgrade replicas are not healthy: ' + states.join(', '));
    }
  });
  const versions = await psql('SELECT count(*) FROM schema_migrations');
  assert(versions === '23', 'concurrent startup did not apply all migrations, found ' + versions);
  for (const replica of replicas) await command('docker', ['rm', '-f', replica], {}, true);
}

async function verifyMigratedData() {
  assert(await psql("SELECT count(*) FROM deployments WHERE id='demo'") === '1', 'deployment was not retained');
  assert(await psql(`SELECT count(*) FROM users WHERE id='${legacyUser}' AND deployment_id='demo'`) === '1', 'legacy user was not retained');
  assert(await psql("SELECT to_regclass('public.enterprises') IS NULL") === 't', 'legacy enterprises table remained');
  assert(await psql("SELECT to_regclass('public.organizations') IS NULL") === 't', 'legacy organizations table remained');
  assert(await psql("SELECT to_regclass('public.agents') IS NULL") === 't', 'legacy agents table remained');
  assert(await psql(`SELECT count(*) FROM user_team_bindings WHERE user_id='${legacyUser}' AND team_id='${legacyOrganization}'`) === '1', 'organization membership was not migrated to a team');
  assert(await psql(`SELECT count(*) FROM skill_assignments WHERE deployment_id='demo' AND skill_id='${legacySkill}' AND subject_type='team' AND subject_id='all-users'`) === '1', 'enterprise Skill grant was not migrated to all-users');
  assert(await psql(`SELECT count(*) FROM skill_assignments WHERE deployment_id='demo' AND skill_id='${legacySkill}' AND subject_type='user' AND subject_id='${legacyUser}'`) === '1', 'Agent Skill grant was not migrated to user');
  assert(await psql("SELECT scope_type || ':' || scope_id FROM control_events WHERE event_id='legacy-event'") === 'team:' + legacyOrganization, 'organization event scope was not migrated to team');
  assert(await psql(`SELECT count(*) FROM licenses WHERE license_id='${legacyLicense}' AND deployment_id='demo'`) === '1', 'legacy License row was not retained');
  assert(await psql("SELECT count(*) FROM information_schema.columns WHERE table_name='licenses' AND column_name IN ('user_limit','activation_limit','agent_limit')") === '0', 'legacy License quota columns remained');
  assert(await psql("SELECT count(*) FROM permissions WHERE id='agents.read'") === '0', 'removed Agent permission remained');
  assert(await psql("SELECT name || ':' || built_in FROM roles WHERE deployment_id='demo' AND id='admin'") === 'Administrator:true', 'built-in admin role was not normalized');
  assert(await psql("SELECT name || ':' || built_in FROM teams WHERE deployment_id='demo' AND id='all-users'") === 'All users:true', 'built-in team was not normalized');
}

async function verifyApiAfterUpgrade() {
  const response = await fetch(baseUrl + '/aep/v1/metadata');
  assert(response.status === 200, 'metadata endpoint was unavailable after upgrade');
  const metadata = await response.json();
  assert(metadata.supportedProtocolVersions?.includes('1.0'), 'metadata contract was unavailable after upgrade');
  const health = await fetch(baseUrl + '/healthz');
  assert(health.status === 200, 'health endpoint was unavailable after upgrade');
}

async function waitForStatus(route, expected) {
  await waitForCommand(async () => {
    const response = await fetch(baseUrl + route);
    if (response.status !== expected) throw new Error(route + ' returned ' + response.status);
  });
}

async function waitForCommand(operation) {
  const deadline = Date.now() + 180_000;
  let lastError;
  while (Date.now() < deadline) {
    try { await operation(); return; } catch (error) { lastError = error; }
    await new Promise(resolve => setTimeout(resolve, 1_000));
  }
  throw new Error('upgrade condition was not met: ' + (lastError?.message ?? 'unknown error'));
}

function psql(query) {
  return commandOutput('docker', [
    'compose', '-p', project, '-f', composeFile, 'exec', '-T', 'postgres',
    'psql', '-U', 'aep', '-d', 'aep', '-Atc', query,
  ], composeEnv);
}

function psqlInput(sql) {
  return commandInput('docker', [
    'compose', '-p', project, '-f', composeFile, 'exec', '-T', 'postgres',
    'psql', '-v', 'ON_ERROR_STOP=1', '-U', 'aep', '-d', 'aep', '-f', '-',
  ], sql, composeEnv);
}

function composeOutput(...args) { return commandOutput('docker', ['compose', '-p', project, '-f', composeFile, ...args], composeEnv); }

function compose(...args) {
  let allowFailure = false;
  if (args.at(-1) === true) { allowFailure = true; args.pop(); }
  return command('docker', ['compose', '-p', project, '-f', composeFile, ...args], composeEnv, allowFailure);
}

function commandOutput(executable, args, extraEnv = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {cwd: root, env: {...process.env, ...extraEnv}, stdio: ['ignore', 'pipe', 'pipe'], shell: false});
    const stdout = []; const stderr = [];
    child.stdout.on('data', chunk => stdout.push(Buffer.from(chunk)));
    child.stderr.on('data', chunk => stderr.push(Buffer.from(chunk)));
    child.on('error', reject);
    child.on('exit', code => {
      const output = Buffer.concat(stdout).toString('utf8').trim();
      const errors = Buffer.concat(stderr).toString('utf8').trim();
      if (code === 0) resolve(output); else reject(new Error(executable + ' ' + args.join(' ') + ' exited with ' + code + ': ' + errors));
    });
  });
}

function commandInput(executable, args, input, extraEnv = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {cwd: root, env: {...process.env, ...extraEnv}, stdio: ['pipe', 'pipe', 'pipe'], shell: false});
    const stderr = [];
    child.stderr.on('data', chunk => stderr.push(Buffer.from(chunk)));
    child.on('error', reject);
    child.on('exit', code => code === 0 ? resolve() : reject(new Error(executable + ' ' + args.join(' ') + ' exited with ' + code + ': ' + Buffer.concat(stderr).toString('utf8'))));
    child.stdin.end(input);
  });
}

function command(executable, args, extraEnv = {}, allowFailure = false) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {cwd: root, env: {...process.env, ...extraEnv}, stdio: 'inherit', shell: false});
    child.on('error', reject);
    child.on('exit', code => code === 0 || allowFailure ? resolve() : reject(new Error(executable + ' ' + args.join(' ') + ' exited with ' + code)));
  });
}

function quote(value) { return "'" + value.replaceAll("'", "''") + "'"; }
function assert(condition, message) { if (!condition) throw new Error(message); }

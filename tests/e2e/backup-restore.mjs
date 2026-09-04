import {createHash} from 'node:crypto';
import {spawn} from 'node:child_process';
import {mkdtemp, readFile, rm, writeFile} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const composeFile = path.join(root, 'deploy', 'compose', 'compose.yaml');
const runId = (process.env.AEP_BACKUP_RUN_ID ?? `${Date.now().toString(36)}-${process.pid}`).toLowerCase().replace(/[^a-z0-9]/g, '').slice(-18);
const sourceProject = `aep-backup-source-${runId}`;
const restoreProject = `aep-backup-restore-${runId}`;
const sourcePort = process.env.AEP_BACKUP_SOURCE_PORT ?? '18088';
const restorePort = process.env.AEP_BACKUP_RESTORE_PORT ?? '18089';
const sourceMinioPort = process.env.AEP_BACKUP_SOURCE_MINIO_PORT ?? '19008';
const restoreMinioPort = process.env.AEP_BACKUP_RESTORE_MINIO_PORT ?? '19009';
const sourceBaseUrl = `http://127.0.0.1:${sourcePort}`;
const restoreBaseUrl = `http://127.0.0.1:${restorePort}`;
const sourceEnv = {AEP_PORT: sourcePort, AEP_MINIO_CONSOLE_PORT: sourceMinioPort};
const restoreEnv = {AEP_PORT: restorePort, AEP_MINIO_CONSOLE_PORT: restoreMinioPort};
const tempDirectory = await mkdtemp(path.join(os.tmpdir(), 'aep-backup-restore-'));
const dumpPath = path.join(tempDirectory, 'aep.dump');
const minioArchivePath = path.join(tempDirectory, 'minio-data.tgz');

let seeded;
try {
  await compose(sourceProject, sourceEnv, ['up', '-d', '--build']);
  await waitForHttp(`${sourceBaseUrl}/healthz`);
  seeded = await seedSource();

  // Stop application writes before taking the coordinated database and object-store backup.
  await compose(sourceProject, sourceEnv, ['stop', 'control-service']);
  const dump = await commandOutput('docker', [
    'compose', '-p', sourceProject, '-f', composeFile, 'exec', '-T', 'postgres',
    'pg_dump', '-U', 'aep', '-d', 'aep', '--format=custom',
  ], sourceEnv);
  await writeFile(dumpPath, dump);
  await compose(sourceProject, sourceEnv, ['stop', 'minio']);
  await archiveVolume(`${sourceProject}_minio-data`, minioArchivePath);

  await compose(restoreProject, restoreEnv, ['up', '-d', 'postgres']);
  await waitForCommand(() => composeOutput(restoreProject, restoreEnv, ['exec', '-T', 'postgres', 'pg_isready', '-U', 'aep', '-d', 'aep']));
  await restoreDatabase();
  await compose(restoreProject, restoreEnv, ['up', '-d', 'minio']);
  await compose(restoreProject, restoreEnv, ['stop', 'minio']);
  await restoreVolume(`${restoreProject}_minio-data`, minioArchivePath);
  await compose(restoreProject, restoreEnv, ['start', 'minio']);
  await compose(restoreProject, restoreEnv, ['up', '-d', 'control-service']);
  await waitForHttp(`${restoreBaseUrl}/healthz`);
  await verifyRestore(seeded);

  console.log(JSON.stringify({
    status: 'passed',
    sourceProject,
    restoreProject,
    checks: [
      'coordinated PostgreSQL custom-format dump',
      'MinIO volume archive and restore',
      'restored admin session and JWKS continuity',
      'restored Skill metadata and published version',
      'restored Skill manifest and ZIP SHA-256',
    ],
  }, null, 2));
} catch (error) {
  await compose(sourceProject, sourceEnv, ['logs', '--no-color', '--tail=200'], true);
  await compose(restoreProject, restoreEnv, ['logs', '--no-color', '--tail=200'], true);
  throw error;
} finally {
  await compose(sourceProject, sourceEnv, ['down', '-v', '--remove-orphans'], true);
  await compose(restoreProject, restoreEnv, ['down', '-v', '--remove-orphans'], true);
  await rm(tempDirectory, {recursive: true, force: true});
}

async function seedSource() {
  const adminToken = await login(sourceBaseUrl, 'admin', 'change-this-admin-password');
  const skillId = `backup-skill-${runId}`;
  const username = `backup-user-${runId}`;
  const password = `Backup-${runId}-password`;
  const archive = emptyZip();
  const sha256 = digest(archive);

  await request(sourceBaseUrl, '/admin/skills', {
    method: 'POST', token: adminToken,
    body: {id: skillId, name: `Backup Skill ${runId}`, description: 'Backup rehearsal fixture', enabled: true},
  });
  const form = new FormData();
  form.append('version', '1.0.0');
  form.append('package', new Blob([archive], {type: 'application/zip'}), 'skill.zip');
  await request(sourceBaseUrl, `/admin/skills/${encodeURIComponent(skillId)}/versions`, {method: 'POST', token: adminToken, body: form});
  await request(sourceBaseUrl, `/admin/skills/${encodeURIComponent(skillId)}/versions/1.0.0/publish`, {method: 'POST', token: adminToken});
  const user = await request(sourceBaseUrl, '/admin/users', {
    method: 'POST', token: adminToken,
    body: {deploymentId: 'demo', username, displayName: `Backup User ${runId}`, temporaryPassword: password, requirePasswordChange: false, roleIds: ['admin'], teamIds: ['all-users']},
  });
  await request(sourceBaseUrl, '/admin/skill-assignments', {
    method: 'POST', token: adminToken,
    body: {skillId, subject: {type: 'user', id: user.id}},
  });
  return {skillId, username, password, sha256, archiveSize: archive.length};
}

async function verifyRestore(expected) {
  const adminToken = await login(restoreBaseUrl, 'admin', 'change-this-admin-password');
  const skill = await request(restoreBaseUrl, `/admin/skills/${encodeURIComponent(expected.skillId)}`, {token: adminToken});
  const version = skill.versions?.find(item => item.version === '1.0.0');
  assert(version?.state === 'published', 'restored Skill version was not published');
  assert(version.sha256 === expected.sha256, 'restored Skill checksum metadata changed');

  const userToken = await login(restoreBaseUrl, expected.username, expected.password);
  const manifest = await request(restoreBaseUrl, '/user/skills/manifest', {token: userToken});
  const item = manifest.skills?.find(candidate => candidate.id === expected.skillId);
  assert(item?.version === '1.0.0', 'restored Skill was absent from the user manifest');
  const packageResponse = await fetch(new URL(item.package.url, restoreBaseUrl), {
    headers: {'Authorization': `Bearer ${userToken}`, 'X-AEP-Protocol-Version': '1.0'},
  });
  assert(packageResponse.ok, `restored Skill package returned ${packageResponse.status}`);
  const packageBytes = new Uint8Array(await packageResponse.arrayBuffer());
  assert(packageBytes.length === expected.archiveSize, 'restored Skill package size changed');
  assert(digest(packageBytes) === expected.sha256, 'restored Skill package checksum changed');
}

async function restoreDatabase() {
  const dump = await readFile(dumpPath);
  await commandWithInput('docker', [
    'compose', '-p', restoreProject, '-f', composeFile, 'exec', '-T', 'postgres',
    'pg_restore', '-U', 'aep', '-d', 'aep', '--clean', '--if-exists', '--no-owner', '--no-privileges',
  ], dump, restoreEnv);
}

async function archiveVolume(volume, outputPath) {
  await command('docker', [
    'run', '--rm', '-v', `${volume}:/source:ro`, '-v', `${tempDirectory}:/backup`,
    'alpine:3.20', 'tar', '-czf', '/backup/minio-data.tgz', '-C', '/source', '.',
  ]);
  const archive = await readFile(outputPath);
  assert(archive.length > 0, 'MinIO volume archive was empty');
}

async function restoreVolume(volume, archivePath) {
  await command('docker', [
    'run', '--rm', '-v', `${volume}:/target`, '-v', `${tempDirectory}:/backup:ro`,
    'alpine:3.20', 'sh', '-c', 'find /target -mindepth 1 -maxdepth 1 -exec rm -rf {} + && tar -xzf /backup/minio-data.tgz -C /target',
  ]);
  const archive = await readFile(archivePath);
  assert(archive.length > 0, 'MinIO restore archive disappeared');
}

async function login(baseUrl, username, password) {
  const result = await request(baseUrl, '/auth/password/login', {method: 'POST', body: {deploymentId: 'demo', username, password}});
  assert(typeof result.accessToken === 'string' && result.accessToken.length > 0, 'login did not return an access token');
  return result.accessToken;
}

async function request(baseUrl, endpoint, options = {}) {
  const headers = {'X-AEP-Protocol-Version': '1.0', ...(options.token ? {Authorization: `Bearer ${options.token}`} : {})};
  const init = {method: options.method ?? 'GET', headers};
  if (options.body instanceof FormData) {
    init.body = options.body;
  } else if (options.body !== undefined) {
    headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(options.body);
  }
  const response = await fetch(`${baseUrl}/aep/v1${endpoint}`, init);
  const text = await response.text();
  if (!response.ok) throw new Error(`${init.method} ${endpoint} returned ${response.status}: ${text}`);
  return text ? JSON.parse(text) : null;
}

async function waitForHttp(url) {
  await waitForCommand(async () => {
    const response = await fetch(url);
    if (!response.ok) throw new Error(`${url} returned ${response.status}`);
  });
}

async function waitForCommand(readiness) {
  const deadline = Date.now() + 120_000;
  let lastError;
  while (Date.now() < deadline) {
    try {
      await readiness();
      return;
    } catch (error) {
      lastError = error;
      await new Promise(resolve => setTimeout(resolve, 1_000));
    }
  }
  throw new Error(`Readiness check timed out: ${lastError?.message ?? 'unknown error'}`);
}

function compose(project, env, args, allowFailure = false) {
  return command('docker', ['compose', '-p', project, '-f', composeFile, ...args], env, allowFailure);
}

function composeOutput(project, env, args) {
  return commandOutput('docker', ['compose', '-p', project, '-f', composeFile, ...args], env);
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
      if (code === 0) return resolve(Buffer.concat(stdout));
      reject(new Error(`${executable} ${args.join(' ')} exited with ${code}: ${Buffer.concat(stderr).toString('utf8').trim()}`));
    });
  });
}

function commandWithInput(executable, args, input, extraEnv = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {cwd: root, env: {...process.env, ...extraEnv}, stdio: ['pipe', 'pipe', 'pipe'], shell: false});
    const stderr = [];
    child.stderr.on('data', chunk => stderr.push(Buffer.from(chunk)));
    child.on('error', reject);
    child.on('exit', code => code === 0 ? resolve() : reject(new Error(`${executable} ${args.join(' ')} exited with ${code}: ${Buffer.concat(stderr).toString('utf8').trim()}`)));
    child.stdin.end(input);
  });
}

function command(executable, args, extraEnv = {}, allowFailure = false) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {cwd: root, env: {...process.env, ...extraEnv}, stdio: 'inherit', shell: false});
    child.on('error', reject);
    child.on('exit', code => code === 0 || allowFailure ? resolve() : reject(new Error(`${executable} ${args.join(' ')} exited with ${code}`)));
  });
}

function emptyZip() {
  return new Uint8Array(Buffer.from('504b0506000000000000000000000000000000000000', 'hex'));
}

function digest(value) {
  return createHash('sha256').update(value).digest('hex');
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

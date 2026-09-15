import {createHash, randomBytes} from 'node:crypto';
import {spawn} from 'node:child_process';
import {lstat, mkdtemp, readFile, rm, writeFile} from 'node:fs/promises';
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
const backupDirectory = path.join(tempDirectory, 'backup');
const encryptionKeyPath = path.join(tempDirectory, 'backup-encryption.key');
const helperImage = 'alpine:3.20';

let seeded;
try {
  await writeFile(encryptionKeyPath, `${randomBytes(32).toString('base64')}\n`, {mode: 0o600});
  await command('docker', ['pull', helperImage]);
  await compose(sourceProject, sourceEnv, ['up', '-d', '--build']);
  await waitForHttp(`${sourceBaseUrl}/healthz`);
  seeded = await seedSource();

  await command(process.execPath, [
    path.join(root, 'scripts', 'backup-compose.mjs'),
    '--project', sourceProject,
    '--output-dir', backupDirectory,
    '--helper-image', helperImage,
    '--encryption-key-file', encryptionKeyPath,
    '--minio-console-port', sourceMinioPort,
  ], sourceEnv);
  await verifyEncryptedBackup();

  await compose(restoreProject, restoreEnv, ['build', 'control-service']);
  await command(process.execPath, [
    path.join(root, 'scripts', 'restore-compose.mjs'),
    '--project', restoreProject,
    '--input-dir', backupDirectory,
    '--helper-image', helperImage,
    '--encryption-key-file', encryptionKeyPath,
    '--port', restorePort,
    '--minio-console-port', restoreMinioPort,
    '--confirm', 'yes',
  ], restoreEnv);
  await verifyRestore(seeded);

  console.log(JSON.stringify({
    status: 'passed',
    sourceProject,
    restoreProject,
    checks: [
      'coordinated PostgreSQL custom-format dump',
      'MinIO volume archive and restore',
      'AES-256-GCM envelope encryption and private file modes',
      'restore helper image trust and pull-never policy',
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

async function verifyEncryptedBackup() {
  const manifest = JSON.parse(await readFile(path.join(backupDirectory, 'manifest.json'), 'utf8'));
  assert(manifest.format === 'aep-backup-v2', 'backup tool did not emit the v2 manifest');
  assert(manifest.encryption?.algorithm === 'aes-256-gcm', 'backup tool did not enable envelope encryption');
  assert(manifest.artifacts?.every(item => item.file.endsWith('.enc')), 'backup tool emitted a plaintext artifact');
  assert(!(await lstat(path.join(backupDirectory, 'postgres.dump')).catch(() => null)), 'plaintext PostgreSQL dump was left on disk');
  assert(!(await lstat(path.join(backupDirectory, 'minio-data.tgz')).catch(() => null)), 'plaintext MinIO archive was left on disk');
  if (process.platform !== 'win32') {
    assert(((await lstat(backupDirectory)).mode & 0o777) === 0o700, 'backup directory mode is not 0700');
    for (const file of ['manifest.json', 'postgres.dump.enc', 'minio-data.tgz.enc']) {
      assert(((await lstat(path.join(backupDirectory, file))).mode & 0o777) === 0o600, `${file} mode is not 0600`);
    }
  }
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

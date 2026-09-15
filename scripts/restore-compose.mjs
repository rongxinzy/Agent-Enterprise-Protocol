import {createHash} from 'node:crypto';
import path from 'node:path';
import {spawn} from 'node:child_process';
import {fileURLToPath} from 'node:url';

import {
  BACKUP_FORMAT,
  assertHelperImage,
  openBackupEnvelope,
  readKeyEncryptionKey,
  readRegularFile,
  recoverArtifact,
} from './backup-security.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const options = parseArgs(process.argv.slice(2));
const project = options.project ?? 'aep-m0';
const composeFiles = (options['compose-file'] ?? 'deploy/compose/compose.yaml').split(',').map(file => path.resolve(root, file));
const inputDir = path.resolve(root, options['input-dir'] ?? 'backups/latest');
const helperImage = assertHelperImage(options['helper-image'] ?? 'alpine:3.20');
const encryptionKeyFile = options['encryption-key-file'] ?? process.env.AEP_BACKUP_ENCRYPTION_KEY_FILE;
const port = options.port ?? process.env.AEP_PORT ?? '8080';
const minioConsolePort = options['minio-console-port'] ?? process.env.AEP_MINIO_CONSOLE_PORT ?? '9001';
const composeEnv = {AEP_PORT: port, AEP_MINIO_CONSOLE_PORT: minioConsolePort};
const composeArgs = ['compose', '-p', project, ...composeFiles.flatMap(file => ['-f', file])];

if (options.confirm !== 'yes') throw new Error('Restore replaces the target database and MinIO volume. Re-run with --confirm yes.');
const manifest = JSON.parse(await readRegularFile(path.join(inputDir, 'manifest.json'), 'utf8'));
if (!['aep-backup-v1', BACKUP_FORMAT].includes(manifest.format) || !Array.isArray(manifest.artifacts) || manifest.artifacts.length !== 2) throw new Error('Unsupported or incomplete backup manifest');
if (manifest.helperImage !== helperImage) throw new Error('Backup helper image does not match the locally approved image');
const keyEncryptionKey = encryptionKeyFile ? await readKeyEncryptionKey(path.resolve(root, encryptionKeyFile)) : null;
let dataKey = null;
let database = null;
let minio = null;
const sensitiveBuffers = [];
try {
  dataKey = openBackupEnvelope(manifest.encryption, keyEncryptionKey);
  const artifacts = new Map();
  for (const item of manifest.artifacts) {
    const name = manifest.format === 'aep-backup-v1' ? item.file : item.name;
    const expectedFile = dataKey ? `${name}.enc` : name;
    if (!['postgres.dump', 'minio-data.tgz'].includes(name)
      || item.file !== expectedFile
      || !/^[a-f0-9]{64}$/.test(item.sha256)
      || !Number.isInteger(item.bytes)
      || item.bytes < 0
      || artifacts.has(name)) throw new Error('Invalid backup manifest artifact');
    const file = path.resolve(inputDir, item.file);
    if (!inside(inputDir, file)) throw new Error(`Missing backup artifact: ${item.file}`);
    const bytes = await readRegularFile(file);
    if (bytes.byteLength !== item.bytes || createHash('sha256').update(bytes).digest('hex') !== item.sha256) {
      bytes.fill(0);
      throw new Error(`Backup checksum mismatch: ${item.file}`);
    }
    let recovered;
    try {
      recovered = recoverArtifact(name, item, bytes, dataKey);
    } catch (error) {
      bytes.fill(0);
      throw error;
    }
    if (recovered !== bytes) bytes.fill(0);
    sensitiveBuffers.push(recovered);
    artifacts.set(name, recovered);
  }
  database = artifacts.get('postgres.dump');
  minio = artifacts.get('minio-data.tgz');
  if (!database || !minio) throw new Error('Backup must contain postgres.dump and minio-data.tgz');

  await command('docker', [...composeArgs, 'stop', 'control-service', 'minio'], true, composeEnv);
  await command('docker', [...composeArgs, 'up', '-d', '--no-build', '--pull', 'never', 'postgres', 'minio'], false, composeEnv);
  await waitForPostgres();
  await commandWithInput('docker', [...composeArgs, 'exec', '-T', 'postgres', 'pg_restore', '-U', 'aep', '-d', 'aep', '--clean', '--if-exists', '--no-owner', '--no-privileges'], database, composeEnv);
  await command('docker', [...composeArgs, 'stop', 'minio'], false, composeEnv);
  await commandWithInput('docker', ['run', '--rm', '--pull', 'never', '-i', '-v', `${project}_minio-data:/target`, helperImage, 'sh', '-c', 'find /target -mindepth 1 -maxdepth 1 -exec rm -rf {} + && tar -xzf - -C /target'], minio);
  await command('docker', [...composeArgs, 'start', 'minio'], false, composeEnv);
  await command('docker', [...composeArgs, 'up', '-d', '--no-build', '--pull', 'never', 'control-service'], false, composeEnv);
  await waitForHttp(`http://127.0.0.1:${port}/readyz`);
  console.log(JSON.stringify({status: 'passed', inputDir, project, baseUrl: `http://127.0.0.1:${port}`}, null, 2));
} finally {
  for (const buffer of sensitiveBuffers) buffer.fill(0);
  dataKey?.fill(0);
  keyEncryptionKey?.fill(0);
}

async function waitForPostgres() {
  const deadline = Date.now() + 120_000;
  let lastError;
  while (Date.now() < deadline) {
    try {
      const result = await commandOutput('docker', [...composeArgs, 'exec', '-T', 'postgres', 'psql', '-U', 'aep', '-d', 'aep', '-Atqc', 'SELECT 1'], composeEnv);
      if (result.toString('utf8').trim() === '1') return;
    } catch (error) { lastError = error; }
    await delay(1_000);
  }
  throw new Error(`Postgres readiness timed out: ${lastError?.message ?? 'unknown error'}`);
}

async function waitForHttp(url) {
  const deadline = Date.now() + 120_000;
  let lastError;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url);
      if (response.ok) return;
      lastError = new Error(`${url} returned ${response.status}`);
    } catch (error) { lastError = error; }
    await delay(1_000);
  }
  throw new Error(`Readiness timed out: ${lastError?.message ?? 'unknown error'}`);
}

function delay(milliseconds) { return new Promise(resolve => setTimeout(resolve, milliseconds)); }
function inside(directory, file) {
  const relative = path.relative(directory, file);
  return relative !== '..' && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative);
}
function parseArgs(args) {
  const result = {};
  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index];
    if (!arg.startsWith('--')) throw new Error(`Unexpected argument: ${arg}`);
    const [key, inline] = arg.slice(2).split('=', 2);
    const value = inline ?? args[++index];
    if (!value || value.startsWith('--')) throw new Error(`Missing value for --${key}`);
    result[key] = value;
  }
  return result;
}
function command(executable, args, allowFailure = false, extraEnv = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {cwd: root, env: {...process.env, ...extraEnv}, stdio: 'inherit', shell: false});
    child.on('error', reject);
    child.on('exit', code => code === 0 || allowFailure ? resolve() : reject(new Error(`${executable} ${args.join(' ')} exited with ${code}`)));
  });
}
function commandOutput(executable, args, extraEnv = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {cwd: root, env: {...process.env, ...extraEnv}, stdio: ['ignore', 'pipe', 'pipe'], shell: false});
    const stdout = [], stderr = [];
    child.stdout.on('data', chunk => stdout.push(Buffer.from(chunk)));
    child.stderr.on('data', chunk => stderr.push(Buffer.from(chunk)));
    child.on('error', reject);
    child.on('exit', code => code === 0
      ? resolve(Buffer.concat(stdout))
      : reject(new Error(`${executable} ${args.join(' ')} exited with ${code}: ${Buffer.concat(stderr).toString('utf8').trim()}`)));
  });
}
function commandWithInput(executable, args, input, extraEnv = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {cwd: root, env: {...process.env, ...extraEnv}, stdio: ['pipe', 'inherit', 'inherit'], shell: false});
    child.on('error', reject);
    child.stdin.end(input);
    child.on('exit', code => code === 0 ? resolve() : reject(new Error(`${executable} ${args.join(' ')} exited with ${code}`)));
  });
}

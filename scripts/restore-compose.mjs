import {createHash} from 'node:crypto';
import {readFile, stat} from 'node:fs/promises';
import path from 'node:path';
import {spawn} from 'node:child_process';
import {fileURLToPath} from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const options = parseArgs(process.argv.slice(2));
const project = options.project ?? 'aep-m0';
const composeFiles = (options['compose-file'] ?? 'deploy/compose/compose.yaml').split(',').map(file => path.resolve(root, file));
const inputDir = path.resolve(root, options['input-dir'] ?? 'backups/latest');
const port = options.port ?? process.env.AEP_PORT ?? '8080';
const minioConsolePort = options['minio-console-port'] ?? process.env.AEP_MINIO_CONSOLE_PORT ?? '9001';
const composeEnv = {AEP_PORT: port, AEP_MINIO_CONSOLE_PORT: minioConsolePort};
const composeArgs = ['compose', '-p', project, ...composeFiles.flatMap(file => ['-f', file])];

if (options.confirm !== 'yes') throw new Error('Restore replaces the target database and MinIO volume. Re-run with --confirm yes.');
const manifest = JSON.parse(await readFile(path.join(inputDir, 'manifest.json'), 'utf8'));
if (manifest.format !== 'aep-backup-v1' || !Array.isArray(manifest.artifacts) || manifest.artifacts.length !== 2) throw new Error('Unsupported or incomplete backup manifest');
const artifacts = new Map();
for (const item of manifest.artifacts) {
  if (!/^[a-f0-9]{64}$/.test(item.sha256) || !Number.isInteger(item.bytes) || !item.file || artifacts.has(item.file)) throw new Error('Invalid backup manifest artifact');
  const file = path.resolve(inputDir, item.file);
  if (!inside(inputDir, file) || !(await stat(file).catch(() => null))?.isFile()) throw new Error(`Missing backup artifact: ${item.file}`);
  const bytes = await readFile(file);
  if (bytes.byteLength !== item.bytes || createHash('sha256').update(bytes).digest('hex') !== item.sha256) throw new Error(`Backup checksum mismatch: ${item.file}`);
  artifacts.set(item.file, bytes);
}
const database = artifacts.get('postgres.dump');
const minio = artifacts.get('minio-data.tgz');
if (!database || !minio) throw new Error('Backup must contain postgres.dump and minio-data.tgz');

await command('docker', [...composeArgs, 'stop', 'control-service', 'minio'], true, composeEnv);
await command('docker', [...composeArgs, 'up', '-d', '--no-build', '--pull', 'never', 'postgres', 'minio'], false, composeEnv);
await waitForPostgres();
await commandWithInput('docker', [...composeArgs, 'exec', '-T', 'postgres', 'pg_restore', '-U', 'aep', '-d', 'aep', '--clean', '--if-exists', '--no-owner', '--no-privileges'], database, composeEnv);
await command('docker', [...composeArgs, 'stop', 'minio'], false, composeEnv);
await commandWithInput('docker', ['run', '--rm', '-i', '-v', `${project}_minio-data:/target`, manifest.helperImage ?? 'alpine:3.20', 'sh', '-c', 'find /target -mindepth 1 -maxdepth 1 -exec rm -rf {} + && tar -xzf - -C /target'], minio);
await command('docker', [...composeArgs, 'start', 'minio'], false, composeEnv);
await command('docker', [...composeArgs, 'up', '-d', '--no-build', '--pull', 'never', 'control-service'], false, composeEnv);
await waitForHttp(`http://127.0.0.1:${port}/readyz`);
console.log(JSON.stringify({status: 'passed', inputDir, project, baseUrl: `http://127.0.0.1:${port}`}, null, 2));

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

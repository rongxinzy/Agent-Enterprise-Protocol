import path from 'node:path';
import {spawn} from 'node:child_process';
import {fileURLToPath} from 'node:url';

import {
  BACKUP_FORMAT,
  assertHelperImage,
  createBackupEnvelope,
  protectArtifact,
  readKeyEncryptionKey,
  secureDirectory,
  writePrivateFile,
} from './backup-security.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const options = parseArgs(process.argv.slice(2));
const project = options.project ?? 'aep-m0';
const composeFiles = (options['compose-file'] ?? 'deploy/compose/compose.yaml').split(',').map(file => path.resolve(root, file));
const outputDir = path.resolve(root, options['output-dir'] ?? path.join('backups', timestamp()));
const helperImage = assertHelperImage(options['helper-image'] ?? 'alpine:3.20');
const encryptionKeyFile = options['encryption-key-file'] ?? process.env.AEP_BACKUP_ENCRYPTION_KEY_FILE;
const minioConsolePort = options['minio-console-port'] ?? process.env.AEP_MINIO_CONSOLE_PORT;
if (minioConsolePort) process.env.AEP_MINIO_CONSOLE_PORT = minioConsolePort;
const composeArgs = ['compose', '-p', project, ...composeFiles.flatMap(file => ['-f', file])];

await secureDirectory(outputDir);
const keyEncryptionKey = encryptionKeyFile ? await readKeyEncryptionKey(path.resolve(root, encryptionKeyFile)) : null;
let envelope = null;
let stopped = false;
let dump = null;
let minioArchive = null;
try {
  envelope = keyEncryptionKey ? createBackupEnvelope(keyEncryptionKey) : null;
  await command('docker', [...composeArgs, 'stop', 'control-service', 'minio']);
  stopped = true;

  dump = await commandOutput('docker', [
    ...composeArgs, 'exec', '-T', 'postgres', 'pg_dump', '-U', 'aep', '-d', 'aep', '--format=custom',
  ]);
  const protectedDatabase = protectArtifact('postgres.dump', dump, envelope?.dataKey ?? null);
  await writePrivateFile(path.join(outputDir, protectedDatabase.manifest.file), protectedDatabase.bytes);
  if (protectedDatabase.bytes !== dump) protectedDatabase.bytes.fill(0);

  const volume = `${project}_minio-data`;
  minioArchive = await commandOutput('docker', [
    'run', '--rm', '--pull', 'never', '-v', `${volume}:/source:ro`, helperImage,
    'tar', '-czf', '-', '-C', '/source', '.',
  ]);
  const protectedMinio = protectArtifact('minio-data.tgz', minioArchive, envelope?.dataKey ?? null);
  await writePrivateFile(path.join(outputDir, protectedMinio.manifest.file), protectedMinio.bytes);
  if (protectedMinio.bytes !== minioArchive) protectedMinio.bytes.fill(0);

  const manifest = {
    format: BACKUP_FORMAT,
    project,
    composeFiles: composeFiles.map(file => path.relative(root, file).replaceAll('\\', '/')),
    createdAt: new Date().toISOString(),
    helperImage,
    encryption: envelope?.manifest ?? null,
    artifacts: [
      protectedDatabase.manifest,
      protectedMinio.manifest,
    ],
  };
  await writePrivateFile(path.join(outputDir, 'manifest.json'), `${JSON.stringify(manifest, null, 2)}\n`);
  console.log(JSON.stringify({status: 'passed', outputDir, project, artifacts: manifest.artifacts}, null, 2));
} finally {
  dump?.fill(0);
  minioArchive?.fill(0);
  envelope?.dataKey.fill(0);
  keyEncryptionKey?.fill(0);
  if (stopped) await command('docker', [...composeArgs, 'start', 'minio', 'control-service'], true);
}

function timestamp() {
  return new Date().toISOString().replaceAll(/[^0-9]/g, '').slice(0, 14);
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

function command(executable, args, allowFailure = false) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {cwd: root, stdio: 'inherit', shell: false});
    child.on('error', reject);
    child.on('exit', code => code === 0 || allowFailure ? resolve() : reject(new Error(`${executable} ${args.join(' ')} exited with ${code}`)));
  });
}

function commandOutput(executable, args) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {cwd: root, stdio: ['ignore', 'pipe', 'pipe'], shell: false});
    const stdout = [], stderr = [];
    child.stdout.on('data', chunk => stdout.push(Buffer.from(chunk)));
    child.stderr.on('data', chunk => stderr.push(Buffer.from(chunk)));
    child.on('error', reject);
    child.on('exit', code => code === 0
      ? resolve(Buffer.concat(stdout))
      : reject(new Error(`${executable} ${args.join(' ')} exited with ${code}: ${Buffer.concat(stderr).toString('utf8').trim()}`)));
  });
}

import {createHash} from 'node:crypto';
import {mkdir, readFile, writeFile} from 'node:fs/promises';
import path from 'node:path';
import {spawn} from 'node:child_process';
import {fileURLToPath} from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const options = parseArgs(process.argv.slice(2));
const project = options.project ?? 'aep-m0';
const composeFiles = (options['compose-file'] ?? 'deploy/compose/compose.yaml').split(',').map(file => path.resolve(root, file));
const outputDir = path.resolve(root, options['output-dir'] ?? path.join('backups', timestamp()));
const helperImage = options['helper-image'] ?? 'alpine:3.20';
const minioConsolePort = options['minio-console-port'] ?? process.env.AEP_MINIO_CONSOLE_PORT;
if (minioConsolePort) process.env.AEP_MINIO_CONSOLE_PORT = minioConsolePort;
const composeArgs = ['compose', '-p', project, ...composeFiles.flatMap(file => ['-f', file])];

await mkdir(outputDir, {recursive: true});
let stopped = false;
try {
  await command('docker', [...composeArgs, 'stop', 'control-service', 'minio']);
  stopped = true;

  const dump = await commandOutput('docker', [
    ...composeArgs, 'exec', '-T', 'postgres', 'pg_dump', '-U', 'aep', '-d', 'aep', '--format=custom',
  ]);
  const databasePath = path.join(outputDir, 'postgres.dump');
  await writeFile(databasePath, dump);

  const volume = `${project}_minio-data`;
  const archive = await commandOutput('docker', [
    'run', '--rm', '-v', `${volume}:/source:ro`, helperImage,
    'tar', '-czf', '-', '-C', '/source', '.',
  ]);
  const minioPath = path.join(outputDir, 'minio-data.tgz');
  await writeFile(minioPath, archive);

  const manifest = {
    format: 'aep-backup-v1',
    project,
    composeFiles: composeFiles.map(file => path.relative(root, file).replaceAll('\\', '/')),
    createdAt: new Date().toISOString(),
    helperImage,
    artifacts: [
      artifact('postgres.dump', dump),
      artifact('minio-data.tgz', archive),
    ],
  };
  await writeFile(path.join(outputDir, 'manifest.json'), `${JSON.stringify(manifest, null, 2)}\n`);
  console.log(JSON.stringify({status: 'passed', outputDir, project, artifacts: manifest.artifacts}, null, 2));
} finally {
  if (stopped) await command('docker', [...composeArgs, 'start', 'minio', 'control-service'], true);
}

function artifact(file, bytes) {
  return {file, bytes: bytes.byteLength, sha256: createHash('sha256').update(bytes).digest('hex')};
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

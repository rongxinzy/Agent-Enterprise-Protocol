import {createHash} from 'node:crypto';
import {readFile, stat} from 'node:fs/promises';
import path from 'node:path';
import {spawn} from 'node:child_process';
import {fileURLToPath} from 'node:url';

const scriptDirectory = path.dirname(fileURLToPath(import.meta.url));
const options = parseArgs(process.argv.slice(2));
const bundleDirectory = path.resolve(options.bundleDir ?? scriptDirectory);
const project = options.project ?? 'aep-offline';
const port = options.port ?? process.env.AEP_PORT ?? '8080';
const dryRun = options.dryRun === true;

const manifest = await readJSON(path.join(bundleDirectory, 'manifest.json'));
validateManifest(manifest);
const profile = options.profile ?? manifest.profile;
if (profile !== manifest.profile) throw new Error(`--profile ${profile} does not match bundle profile ${manifest.profile}`);

const imageArchives = await verifyArchives(bundleDirectory, manifest);
const composeFiles = manifest.composeFiles.map(file => path.join(bundleDirectory, file));
for (const file of composeFiles) {
  if (!inside(bundleDirectory, file)) throw new Error(`Compose file escapes the Bundle directory: ${file}`);
  await requireFile(file);
}
const composeArgs = composeFiles.flatMap(file => ['-f', file]);
const composeEnvironment = {AEP_PORT: port};

console.log(JSON.stringify({status: 'validated', bundleDirectory, profile, project, images: imageArchives.map(item => item.archive), dryRun}, null, 2));
if (dryRun) {
  console.log(JSON.stringify({status: 'dry-run', load: imageArchives.map(item => ['docker', 'load', '--input', item.path]), compose: ['docker', 'compose', '-p', project, ...composeArgs, 'up', '-d', '--no-build', '--pull', 'never']}, null, 2));
  process.exit(0);
}

for (const archive of imageArchives) {
  await command('docker', ['load', '--input', archive.path], composeEnvironment, bundleDirectory);
}
await command('docker', ['compose', '-p', project, ...composeArgs, 'up', '-d', '--no-build', '--pull', 'never'], composeEnvironment, bundleDirectory);
await waitForHealth(`http://127.0.0.1:${port}/readyz`);
console.log(JSON.stringify({status: 'started', profile, project, baseUrl: `http://127.0.0.1:${port}`}, null, 2));

async function verifyArchives(directory, bundle) {
  const checksums = parseChecksums(await readFile(path.join(directory, 'SHA256SUMS'), 'utf8'));
  const archives = [];
  for (const image of bundle.images) {
    const archive = image.archive.replaceAll('/', path.sep);
    const archivePath = path.resolve(directory, archive);
    if (!inside(directory, archivePath)) throw new Error(`Bundle archive escapes its directory: ${image.archive}`);
    await requireFile(archivePath);
    const expected = checksums.get(image.archive);
    if (!expected) throw new Error(`SHA256SUMS is missing ${image.archive}`);
    const actual = await sha256(archivePath);
    if (actual !== expected || actual !== image.archiveSha256) {
      throw new Error(`SHA-256 mismatch for ${image.archive}`);
    }
    archives.push({archive: image.archive, path: archivePath});
  }
  return archives;
}

function validateManifest(bundle) {
  if (bundle.format !== 'aep-offline-bundle-v1') throw new Error('unsupported offline Bundle format');
  if (!['base', 'gateway'].includes(bundle.profile)) throw new Error('offline Bundle has an invalid profile');
  if (!Array.isArray(bundle.images) || bundle.images.length === 0) throw new Error('offline Bundle has no images');
  if (!Array.isArray(bundle.composeFiles) || !bundle.composeFiles.includes('deploy/compose/offline.yaml')) throw new Error('offline Bundle is missing offline Compose configuration');
  for (const image of bundle.images) {
    if (!image.archive || !image.archiveSha256 || !/^[a-f0-9]{64}$/.test(image.archiveSha256)) throw new Error('offline Bundle has an invalid image checksum');
  }
}

function parseChecksums(content) {
  const checksums = new Map();
  for (const line of content.split(/\r?\n/).map(value => value.trim()).filter(Boolean)) {
    const match = line.match(/^([a-f0-9]{64})\s+(?:\*|)(.+)$/);
    if (!match) throw new Error(`invalid SHA256SUMS entry: ${line}`);
    checksums.set(match[2].replaceAll('\\', '/'), match[1]);
  }
  return checksums;
}

async function sha256(file) {
  const content = await readFile(file);
  return createHash('sha256').update(content).digest('hex');
}

async function readJSON(file) {
  return JSON.parse(await readFile(file, 'utf8'));
}

async function requireFile(file) {
  const details = await stat(file).catch(() => null);
  if (!details?.isFile()) throw new Error(`required Bundle file is missing: ${file}`);
}

function inside(directory, file) {
  const relative = path.relative(directory, file);
  return relative !== '..' && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative);
}

async function waitForHealth(url) {
  const deadline = Date.now() + 180_000;
  let lastError;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url);
      if (response.ok) return;
      lastError = new Error(`readiness returned ${response.status}`);
    } catch (error) { lastError = error; }
    await new Promise(resolve => setTimeout(resolve, 1_000));
  }
  throw new Error(`offline control service did not become ready: ${lastError?.message ?? 'unknown error'}`);
}

function parseArgs(args) {
  const result = {};
  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index];
    if (arg === '--dry-run') { result.dryRun = true; continue; }
    if (!arg.startsWith('--')) throw new Error(`unexpected argument: ${arg}`);
    const [rawKey, inline] = arg.slice(2).split('=', 2);
    const value = inline ?? args[++index];
    if (!value || value.startsWith('--')) throw new Error(`missing value for --${rawKey}`);
    const key = rawKey === 'bundle-dir' ? 'bundleDir'
      : rawKey === 'dry-run' ? 'dryRun'
        : rawKey;
    result[key] = value;
  }
  return result;
}

function command(executable, args, extraEnv, cwd) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {cwd, env: {...process.env, ...extraEnv}, stdio: 'inherit', shell: false});
    child.on('error', reject);
    child.on('exit', code => code === 0 ? resolve() : reject(new Error(`${executable} ${args.join(' ')} exited with ${code}`)));
  });
}

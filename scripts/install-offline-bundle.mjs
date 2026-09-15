import {createHash, createPublicKey, verify} from 'node:crypto';
import {createReadStream} from 'node:fs';
import {lstat, readFile, readdir, realpath} from 'node:fs/promises';
import path from 'node:path';
import {spawn} from 'node:child_process';
import {fileURLToPath} from 'node:url';

const scriptDirectory = path.dirname(fileURLToPath(import.meta.url));
const options = parseArgs(process.argv.slice(2));
const bundleDirectory = path.resolve(options.bundleDir ?? scriptDirectory);
const project = options.project ?? 'aep-offline';
const port = options.port ?? process.env.AEP_PORT ?? '8080';
const dryRun = options.dryRun === true;
const trustedPublicKeyFile = options.trustedPublicKey ?? process.env.AEP_OFFLINE_TRUSTED_PUBLIC_KEY_FILE;
if (!trustedPublicKeyFile) throw new Error('--trusted-public-key is required');
const realBundleDirectory = await realpath(bundleDirectory);
const publicKeyFile = path.resolve(trustedPublicKeyFile);
const realPublicKeyFile = await realpath(publicKeyFile).catch(() => publicKeyFile);
if (inside(realBundleDirectory, realPublicKeyFile)) {
  throw new Error('the trusted public key must be provisioned outside the Bundle directory');
}

const checksumFile = path.join(bundleDirectory, 'SHA256SUMS');
const signatureFile = path.join(bundleDirectory, 'SHA256SUMS.sig');
const checksumContent = await readFile(checksumFile);
const signature = await readFile(signatureFile);
const publicKey = createPublicKey(await readFile(publicKeyFile));
if (publicKey.asymmetricKeyType !== 'ed25519') throw new Error('the trusted public key must be Ed25519');
if (!verify(null, checksumContent, publicKey, signature)) throw new Error('offline Bundle signature verification failed');
const checksums = parseChecksums(checksumContent.toString('utf8'));
const manifestContent = await readVerifiedFile(bundleDirectory, 'manifest.json', checksums.get('manifest.json'));
const manifest = JSON.parse(manifestContent.toString('utf8'));
validateManifest(manifest);
await verifyExactFileSet(bundleDirectory, new Set(['manifest.json', 'SHA256SUMS', 'SHA256SUMS.sig', ...manifest.files.map(file => file.path)]));
const profile = options.profile ?? manifest.profile;
if (profile !== manifest.profile) throw new Error(`--profile ${profile} does not match bundle profile ${manifest.profile}`);

const verifiedFiles = await verifyBundleFiles(bundleDirectory, manifest, checksums);
const imageArchives = manifest.images.map(image => ({archive: image.archive, path: verifiedFiles.get(image.archive)}));
const composeFiles = manifest.composeFiles.map(file => path.join(bundleDirectory, file));
for (const file of composeFiles) {
  if (!inside(bundleDirectory, file)) throw new Error(`Compose file escapes the Bundle directory: ${file}`);
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

async function verifyBundleFiles(directory, bundle, checksums) {
  const expectedPaths = new Set(['manifest.json', ...bundle.files.map(file => file.path)]);
  if (checksums.size !== expectedPaths.size || [...checksums.keys()].some(file => !expectedPaths.has(file))) {
    throw new Error('SHA256SUMS does not exactly match the signed Bundle manifest');
  }
  const verified = new Map();
  for (const file of bundle.files) {
    const expected = checksums.get(file.path);
    if (expected !== file.sha256) throw new Error(`checksum metadata mismatch for ${file.path}`);
    const filePath = path.resolve(directory, file.path.replaceAll('/', path.sep));
    await verifyFile(filePath, directory, expected, file.bytes, file.path);
    verified.set(file.path, filePath);
  }
  return verified;
}

function validateManifest(bundle) {
  if (bundle.format !== 'aep-offline-bundle-v2') throw new Error('unsupported offline Bundle format');
  if (!['base', 'gateway'].includes(bundle.profile)) throw new Error('offline Bundle has an invalid profile');
  if (!Array.isArray(bundle.images) || bundle.images.length === 0) throw new Error('offline Bundle has no images');
  if (!Array.isArray(bundle.composeFiles) || !bundle.composeFiles.includes('deploy/compose/offline.yaml')) throw new Error('offline Bundle is missing offline Compose configuration');
  if (bundle.signature?.algorithm !== 'Ed25519' || bundle.signature?.payload !== 'SHA256SUMS' || bundle.signature?.file !== 'SHA256SUMS.sig') {
    throw new Error('offline Bundle has an invalid signature contract');
  }
  if (!Array.isArray(bundle.files) || bundle.files.length === 0) throw new Error('offline Bundle has no signed files');
  const filePaths = new Set();
  for (const file of bundle.files) {
    validateRelativePath(file.path);
    if (filePaths.has(file.path)) throw new Error(`offline Bundle has a duplicate file: ${file.path}`);
    if (!/^[a-f0-9]{64}$/.test(file.sha256) || !Number.isSafeInteger(file.bytes) || file.bytes < 0) {
      throw new Error(`offline Bundle has invalid file metadata: ${file.path}`);
    }
    filePaths.add(file.path);
  }
  for (const required of ['install-offline-bundle.mjs', 'OFFLINE-README.md', ...bundle.composeFiles]) {
    if (!filePaths.has(required)) throw new Error(`offline Bundle manifest is missing ${required}`);
  }
  for (const image of bundle.images) {
    validateRelativePath(image.archive);
    const file = bundle.files.find(item => item.path === image.archive);
    if (!file || file.sha256 !== image.archiveSha256 || file.bytes !== image.archiveBytes) throw new Error('offline Bundle has invalid image metadata');
  }
}

function validateRelativePath(file) {
  if (typeof file !== 'string' || file.length === 0 || file.includes('\\') || path.posix.isAbsolute(file) || path.posix.normalize(file) !== file || file.startsWith('../')) {
    throw new Error(`offline Bundle has an invalid file path: ${file}`);
  }
  if (['manifest.json', 'SHA256SUMS', 'SHA256SUMS.sig'].includes(file)) throw new Error(`offline Bundle file list contains reserved metadata: ${file}`);
}

async function verifyExactFileSet(directory, expected, prefix = '') {
  const entries = await readdir(path.join(directory, prefix), {withFileTypes: true});
  for (const entry of entries) {
    const relative = path.posix.join(prefix.replaceAll(path.sep, '/'), entry.name);
    if (entry.isSymbolicLink()) throw new Error(`offline Bundle contains a symbolic link: ${relative}`);
    if (entry.isDirectory()) {
      await verifyExactFileSet(directory, expected, relative);
      continue;
    }
    if (!entry.isFile()) throw new Error(`offline Bundle contains an unsupported filesystem entry: ${relative}`);
    if (!expected.delete(relative)) throw new Error(`offline Bundle contains an unsigned file: ${relative}`);
  }
  if (prefix === '' && expected.size > 0) throw new Error(`offline Bundle is missing signed files: ${[...expected].join(', ')}`);
}

function parseChecksums(content) {
  const checksums = new Map();
  for (const line of content.split(/\r?\n/).map(value => value.trim()).filter(Boolean)) {
    const match = line.match(/^([a-f0-9]{64})\s+(?:\*|)(.+)$/);
    if (!match) throw new Error(`invalid SHA256SUMS entry: ${line}`);
    const file = match[2].replaceAll('\\', '/');
    if (checksums.has(file)) throw new Error(`duplicate SHA256SUMS entry: ${file}`);
    checksums.set(file, match[1]);
  }
  return checksums;
}

async function readVerifiedFile(directory, relative, expected) {
  if (!expected) throw new Error(`SHA256SUMS is missing ${relative}`);
  const file = path.resolve(directory, relative.replaceAll('/', path.sep));
  await verifyFile(file, directory, expected, undefined, relative);
  return readFile(file);
}

async function verifyFile(file, directory, expectedHash, expectedBytes, displayName) {
  if (!inside(directory, file)) throw new Error(`Bundle file escapes its directory: ${displayName}`);
  const entry = await lstat(file).catch(() => null);
  if (!entry?.isFile() || entry.isSymbolicLink()) throw new Error(`required Bundle file is missing or unsafe: ${displayName}`);
  const resolved = await realpath(file);
  const resolvedDirectory = await realpath(directory);
  if (!inside(resolvedDirectory, resolved)) throw new Error(`Bundle file resolves outside its directory: ${displayName}`);
  if (expectedBytes !== undefined && entry.size !== expectedBytes) throw new Error(`size mismatch for ${displayName}`);
  const hash = createHash('sha256');
  await new Promise((resolve, reject) => {
    const stream = createReadStream(file);
    stream.on('data', chunk => hash.update(chunk));
    stream.on('error', reject);
    stream.on('end', resolve);
  });
  if (hash.digest('hex') !== expectedHash) throw new Error(`SHA-256 mismatch for ${displayName}`);
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
      : rawKey === 'trusted-public-key' ? 'trustedPublicKey'
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

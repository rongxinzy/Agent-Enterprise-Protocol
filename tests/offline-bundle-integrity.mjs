import assert from 'node:assert/strict';
import {createHash, generateKeyPairSync, sign} from 'node:crypto';
import {spawn} from 'node:child_process';
import {mkdir, mkdtemp, readFile, rm, writeFile} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const fixture = await mkdtemp(path.join(os.tmpdir(), 'aep-offline-integrity-'));
const bundle = path.join(fixture, 'bundle');
const trustedPublicKey = path.join(fixture, 'offline-release.pub.pem');

try {
  await mkdir(path.join(bundle, 'deploy', 'compose'), {recursive: true});
  await mkdir(path.join(bundle, 'images'), {recursive: true});
  const payloads = new Map([
    ['install-offline-bundle.mjs', 'installer payload\n'],
    ['OFFLINE-README.md', 'offline instructions\n'],
    ['deploy/compose/compose.yaml', 'services: {}\n'],
    ['deploy/compose/offline.yaml', 'services: {}\n'],
    ['images/01-control.tar', Buffer.from([1, 2, 3, 4])],
  ]);
  for (const [relative, content] of payloads) {
    await writeFile(path.join(bundle, relative.replaceAll('/', path.sep)), content);
  }

  const files = await Promise.all([...payloads.keys()].sort().map(relative => describe(path.join(bundle, relative), relative)));
  const imageFile = files.find(file => file.path === 'images/01-control.tar');
  const manifest = {
    format: 'aep-offline-bundle-v2',
    protocolVersion: '1.0',
    packageVersion: '0.1.0',
    profile: 'base',
    generatedAt: new Date(0).toISOString(),
    services: {'control-service': 'aep-control-service:test'},
    composeFiles: ['deploy/compose/compose.yaml', 'deploy/compose/offline.yaml'],
    images: [{
      reference: 'aep-control-service:test',
      digest: 'sha256:test',
      imageId: 'sha256:test',
      archive: imageFile.path,
      archiveSha256: imageFile.sha256,
      archiveBytes: imageFile.bytes,
    }],
    signature: {algorithm: 'Ed25519', payload: 'SHA256SUMS', file: 'SHA256SUMS.sig'},
    files,
  };
  await writeFile(path.join(bundle, 'manifest.json'), `${JSON.stringify(manifest, null, 2)}\n`);
  const signedFiles = [await describe(path.join(bundle, 'manifest.json'), 'manifest.json'), ...files]
    .sort((left, right) => left.path.localeCompare(right.path));
  const checksumContent = Buffer.from(`${signedFiles.map(file => `${file.sha256}  ${file.path}`).join('\n')}\n`);
  await writeFile(path.join(bundle, 'SHA256SUMS'), checksumContent);

  const {privateKey, publicKey} = generateKeyPairSync('ed25519');
  await writeFile(trustedPublicKey, publicKey.export({type: 'spki', format: 'pem'}));
  const signatureFile = path.join(bundle, 'SHA256SUMS.sig');
  await writeFile(signatureFile, sign(null, checksumContent, privateKey));

  const valid = await installer(['--bundle-dir', bundle, '--trusted-public-key', trustedPublicKey, '--dry-run']);
  assert.match(valid.stdout, /"status": "validated"/);
  assert.match(valid.stdout, /"status": "dry-run"/);

  await writeFile(path.join(bundle, 'deploy', 'compose', 'compose.yaml'), 'services:\n  attacker: {}\n');
  await assert.rejects(
    installer(['--bundle-dir', bundle, '--trusted-public-key', trustedPublicKey, '--dry-run']),
    /size mismatch|SHA-256 mismatch/,
  );
  await writeFile(path.join(bundle, 'deploy', 'compose', 'compose.yaml'), payloads.get('deploy/compose/compose.yaml'));

  await writeFile(path.join(bundle, 'SHA256SUMS'), Buffer.concat([checksumContent, Buffer.from('# tampered\n')]));
  await assert.rejects(
    installer(['--bundle-dir', bundle, '--trusted-public-key', trustedPublicKey, '--dry-run']),
    /signature verification failed/,
  );
  await writeFile(path.join(bundle, 'SHA256SUMS'), checksumContent);

  const incompleteChecksum = Buffer.from(`${signedFiles.filter(file => file.path !== 'manifest.json').map(file => `${file.sha256}  ${file.path}`).join('\n')}\n`);
  await writeFile(path.join(bundle, 'SHA256SUMS'), incompleteChecksum);
  await writeFile(signatureFile, sign(null, incompleteChecksum, privateKey));
  await assert.rejects(
    installer(['--bundle-dir', bundle, '--trusted-public-key', trustedPublicKey, '--dry-run']),
    /SHA256SUMS is missing manifest.json/,
  );
  await writeFile(path.join(bundle, 'SHA256SUMS'), checksumContent);
  await writeFile(signatureFile, sign(null, checksumContent, privateKey));

  await writeFile(path.join(bundle, '.env'), 'AEP_CONTROL_SERVICE_IMAGE=attacker/image\n');
  await assert.rejects(
    installer(['--bundle-dir', bundle, '--trusted-public-key', trustedPublicKey, '--dry-run']),
    /contains an unsigned file: \.env/,
  );
  await rm(path.join(bundle, '.env'));

  await assert.rejects(installer(['--bundle-dir', bundle, '--dry-run']), /--trusted-public-key is required/);
  const bundledPublicKey = path.join(bundle, 'untrusted.pub.pem');
  await writeFile(bundledPublicKey, await readFile(trustedPublicKey));
  await assert.rejects(
    installer(['--bundle-dir', bundle, '--trusted-public-key', bundledPublicKey, '--dry-run']),
    /must be provisioned outside/,
  );

  console.log('AEP offline Bundle signature and integrity tests passed.');
} finally {
  await rm(fixture, {recursive: true, force: true});
}

async function describe(file, relative) {
  const content = await readFile(file);
  return {path: relative, sha256: createHash('sha256').update(content).digest('hex'), bytes: content.byteLength};
}

function installer(args) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [path.join(root, 'scripts', 'install-offline-bundle.mjs'), ...args], {
      cwd: root,
      stdio: ['ignore', 'pipe', 'pipe'],
      shell: false,
    });
    const stdout = [];
    const stderr = [];
    child.stdout.on('data', chunk => stdout.push(Buffer.from(chunk)));
    child.stderr.on('data', chunk => stderr.push(Buffer.from(chunk)));
    child.on('error', reject);
    child.on('exit', code => {
      const result = {
        stdout: Buffer.concat(stdout).toString('utf8'),
        stderr: Buffer.concat(stderr).toString('utf8'),
      };
      if (code === 0) resolve(result);
      else reject(new Error(result.stderr || `offline installer exited with ${code}`));
    });
  });
}

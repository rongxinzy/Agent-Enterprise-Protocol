import assert from 'node:assert/strict';
import {randomBytes} from 'node:crypto';
import {lstat, mkdtemp, rm, writeFile} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';

import {
  assertHelperImage,
  createBackupEnvelope,
  openBackupEnvelope,
  protectArtifact,
  readKeyEncryptionKey,
  readRegularFile,
  recoverArtifact,
  secureDirectory,
  writePrivateFile,
} from '../scripts/backup-security.mjs';

const temporaryDirectory = await mkdtemp(path.join(os.tmpdir(), 'aep-backup-security-'));
const keyFile = path.join(temporaryDirectory, 'backup.key');
const privateDirectory = path.join(temporaryDirectory, 'backup');
const privateFile = path.join(privateDirectory, 'manifest.json');
const source = Buffer.from('database and credential ciphertext fixture', 'utf8');
const keyMaterial = randomBytes(32);
let loadedKey = null;
let dataKey = null;
let recoveredKey = null;
let recovered = null;

try {
  await writeFile(keyFile, `${keyMaterial.toString('base64')}\n`, {mode: 0o600});
  loadedKey = await readKeyEncryptionKey(keyFile);
  assert.deepEqual(loadedKey, keyMaterial);

  const envelope = createBackupEnvelope(loadedKey);
  dataKey = envelope.dataKey;
  recoveredKey = openBackupEnvelope(envelope.manifest, loadedKey);
  assert.deepEqual(recoveredKey, dataKey);

  const protectedArtifact = protectArtifact('postgres.dump', source, dataKey);
  assert.equal(protectedArtifact.manifest.file, 'postgres.dump.enc');
  assert.notDeepEqual(protectedArtifact.bytes, source);
  recovered = recoverArtifact('postgres.dump', protectedArtifact.manifest, protectedArtifact.bytes, recoveredKey);
  assert.deepEqual(recovered, source);

  const tampered = Buffer.from(protectedArtifact.bytes);
  tampered[0] ^= 0xff;
  assert.throws(
    () => recoverArtifact('postgres.dump', protectedArtifact.manifest, tampered, recoveredKey),
    /authentication failed/,
  );
  tampered.fill(0);

  const wrongKey = randomBytes(32);
  assert.throws(() => openBackupEnvelope(envelope.manifest, wrongKey), /metadata or key is invalid/);
  assert.throws(() => openBackupEnvelope(null, loadedKey), /downgrade detected/);
  wrongKey.fill(0);

  assert.equal(assertHelperImage('alpine:3.20'), 'alpine:3.20');
  assert.equal(assertHelperImage(`registry.example/aep/backup@sha256:${'a'.repeat(64)}`), `registry.example/aep/backup@sha256:${'a'.repeat(64)}`);
  for (const invalid of ['alpine', '--privileged', 'alpine latest', '../../image:latest']) {
    assert.throws(() => assertHelperImage(invalid), /explicit OCI image reference/);
  }

  await secureDirectory(privateDirectory);
  await writePrivateFile(privateFile, '{}\n');
  assert.equal(await readRegularFile(privateFile, 'utf8'), '{}\n');
  if (process.platform !== 'win32') {
    assert.equal((await lstat(privateDirectory)).mode & 0o777, 0o700);
    assert.equal((await lstat(privateFile)).mode & 0o777, 0o600);
  }

  console.log(JSON.stringify({
    status: 'passed',
    checks: [
      'AES-256-GCM envelope encryption round trip',
      'tamper and encryption downgrade rejection',
      'explicit helper image validation',
      'private backup directory and file modes',
    ],
  }, null, 2));
} finally {
  source.fill(0);
  keyMaterial.fill(0);
  loadedKey?.fill(0);
  dataKey?.fill(0);
  recoveredKey?.fill(0);
  recovered?.fill(0);
  await rm(temporaryDirectory, {recursive: true, force: true});
}

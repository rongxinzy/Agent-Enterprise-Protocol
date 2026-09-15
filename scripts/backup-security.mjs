import {createCipheriv, createDecipheriv, createHash, randomBytes} from 'node:crypto';
import {chmod, lstat, mkdir, readFile, writeFile} from 'node:fs/promises';

export const BACKUP_FORMAT = 'aep-backup-v2';
export const BACKUP_ENCRYPTION_ALGORITHM = 'aes-256-gcm';

const ARTIFACT_AAD_PREFIX = `${BACKUP_FORMAT}:artifact:`;
const DATA_KEY_AAD = Buffer.from(`${BACKUP_FORMAT}:data-key`, 'utf8');
const HELPER_IMAGE_PATTERN = /^(?=.{1,255}$)(?:[a-z0-9]+(?:[._-][a-z0-9]+)*(?::[0-9]+)?\/)*(?:[a-z0-9]+(?:[._-][a-z0-9]+)*)(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?(?:@sha256:[a-f0-9]{64})?$/;

export function assertHelperImage(image) {
  const leaf = typeof image === 'string' ? image.slice(image.lastIndexOf('/') + 1) : '';
  if (typeof image !== 'string'
    || !HELPER_IMAGE_PATTERN.test(image)
    || (!leaf.includes(':') && !image.includes('@sha256:'))) {
    throw new Error('Backup helper image must be a valid explicit OCI image reference');
  }
  return image;
}

export async function secureDirectory(directory) {
  await mkdir(directory, {recursive: true, mode: 0o700});
  await chmod(directory, 0o700);
}

export async function writePrivateFile(file, data) {
  await writeFile(file, data, {mode: 0o600});
  await chmod(file, 0o600);
}

export async function readRegularFile(file, encoding) {
  const metadata = await lstat(file).catch(() => null);
  if (!metadata?.isFile() || metadata.isSymbolicLink()) throw new Error(`Expected a regular file: ${file}`);
  return readFile(file, encoding);
}

export async function readKeyEncryptionKey(file) {
  const encoded = (await readRegularFile(file, 'utf8')).trim();
  const key = decodeBase64(encoded, 32, 'backup encryption key');
  return key;
}

export function createBackupEnvelope(keyEncryptionKey) {
  assertKey(keyEncryptionKey, 'key encryption key');
  const dataKey = randomBytes(32);
  const nonce = randomBytes(12);
  const cipher = createCipheriv(BACKUP_ENCRYPTION_ALGORITHM, keyEncryptionKey, nonce);
  cipher.setAAD(DATA_KEY_AAD);
  const wrappedDataKey = Buffer.concat([cipher.update(dataKey), cipher.final()]);
  const authTag = cipher.getAuthTag();
  return {
    dataKey,
    manifest: {
      algorithm: BACKUP_ENCRYPTION_ALGORITHM,
      keyWrap: BACKUP_ENCRYPTION_ALGORITHM,
      keyId: encryptionKeyId(keyEncryptionKey),
      wrappedDataKey: wrappedDataKey.toString('base64'),
      nonce: nonce.toString('base64'),
      authTag: authTag.toString('base64'),
    },
  };
}

export function openBackupEnvelope(metadata, keyEncryptionKey) {
  if (metadata === null || metadata === undefined) {
    if (keyEncryptionKey) throw new Error('Backup encryption downgrade detected');
    return null;
  }
  if (!keyEncryptionKey) throw new Error('Encrypted backup requires --encryption-key-file');
  assertKey(keyEncryptionKey, 'key encryption key');
  if (!isRecord(metadata)
    || metadata.algorithm !== BACKUP_ENCRYPTION_ALGORITHM
    || metadata.keyWrap !== BACKUP_ENCRYPTION_ALGORITHM
    || metadata.keyId !== encryptionKeyId(keyEncryptionKey)) {
    throw new Error('Backup encryption metadata or key is invalid');
  }
  const nonce = decodeBase64(metadata.nonce, 12, 'backup key-wrap nonce');
  const authTag = decodeBase64(metadata.authTag, 16, 'backup key-wrap authentication tag');
  const wrappedDataKey = decodeBase64(metadata.wrappedDataKey, 32, 'wrapped backup data key');
  try {
    const decipher = createDecipheriv(BACKUP_ENCRYPTION_ALGORITHM, keyEncryptionKey, nonce);
    decipher.setAAD(DATA_KEY_AAD);
    decipher.setAuthTag(authTag);
    const dataKey = Buffer.concat([decipher.update(wrappedDataKey), decipher.final()]);
    assertKey(dataKey, 'backup data key');
    return dataKey;
  } catch {
    throw new Error('Backup encryption metadata or key is invalid');
  } finally {
    nonce.fill(0);
    authTag.fill(0);
    wrappedDataKey.fill(0);
  }
}

export function protectArtifact(name, bytes, dataKey) {
  if (!dataKey) {
    return {
      bytes,
      manifest: artifactManifest(name, name, bytes),
    };
  }
  assertKey(dataKey, 'backup data key');
  const nonce = randomBytes(12);
  const cipher = createCipheriv(BACKUP_ENCRYPTION_ALGORITHM, dataKey, nonce);
  cipher.setAAD(Buffer.from(`${ARTIFACT_AAD_PREFIX}${name}`, 'utf8'));
  const encrypted = Buffer.concat([cipher.update(bytes), cipher.final()]);
  const authTag = cipher.getAuthTag();
  return {
    bytes: encrypted,
    manifest: {
      ...artifactManifest(name, `${name}.enc`, encrypted),
      nonce: nonce.toString('base64'),
      authTag: authTag.toString('base64'),
    },
  };
}

export function recoverArtifact(name, item, bytes, dataKey) {
  if (!dataKey) return bytes;
  assertKey(dataKey, 'backup data key');
  if (!isRecord(item)) throw new Error(`Invalid encrypted backup artifact: ${name}`);
  const nonce = decodeBase64(item.nonce, 12, `${name} nonce`);
  const authTag = decodeBase64(item.authTag, 16, `${name} authentication tag`);
  try {
    const decipher = createDecipheriv(BACKUP_ENCRYPTION_ALGORITHM, dataKey, nonce);
    decipher.setAAD(Buffer.from(`${ARTIFACT_AAD_PREFIX}${name}`, 'utf8'));
    decipher.setAuthTag(authTag);
    return Buffer.concat([decipher.update(bytes), decipher.final()]);
  } catch {
    throw new Error(`Encrypted backup artifact authentication failed: ${name}`);
  } finally {
    nonce.fill(0);
    authTag.fill(0);
  }
}

export function encryptionKeyId(key) {
  assertKey(key, 'key encryption key');
  return `sha256:${createHash('sha256').update(key).digest('hex')}`;
}

function artifactManifest(name, file, bytes) {
  return {
    name,
    file,
    bytes: bytes.byteLength,
    sha256: createHash('sha256').update(bytes).digest('hex'),
  };
}

function decodeBase64(value, length, label) {
  if (typeof value !== 'string' || !/^[A-Za-z0-9+/]+={0,2}$/.test(value)) throw new Error(`Invalid ${label}`);
  const decoded = Buffer.from(value, 'base64');
  const expected = Buffer.from(decoded).toString('base64');
  if (decoded.byteLength !== length || expected !== value) {
    decoded.fill(0);
    throw new Error(`Invalid ${label}`);
  }
  return decoded;
}

function assertKey(key, label) {
  if (!Buffer.isBuffer(key) || key.byteLength !== 32) throw new Error(`Invalid ${label}`);
}

function isRecord(value) {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

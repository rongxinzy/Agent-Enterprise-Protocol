import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {mkdtemp, readFile, rm, writeFile} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const fixture = await mkdtemp(path.join(os.tmpdir(), 'aep-release-artifacts-'));
const version = '9.8.7';
const commit = '0123456789abcdef0123456789abcdef01234567';
const expected = [
  `aep-offline-base-v${version}.tar.gz`,
  `aep-offline-gateway-v${version}.tar.gz`,
  'aep-foundation.source.sbom.cdx.json',
  'aep-control-service.image.sbom.cdx.json',
  'aep-gateway-authorizer.image.sbom.cdx.json',
  'aep-gateway-reconciler.image.sbom.cdx.json',
];

try {
  for (const file of expected) {
    const content = file.endsWith('.sbom.cdx.json')
      ? JSON.stringify({bomFormat: 'CycloneDX', specVersion: '1.6', components: []})
      : Buffer.from([0x1f, 0x8b, 0x08, 0x00]);
    await writeFile(path.join(fixture, file), content);
  }
  await command(['--artifact-dir', fixture, '--version', version, '--commit', commit]);
  const manifest = JSON.parse(await readFile(path.join(fixture, 'release-manifest.json'), 'utf8'));
  assert.equal(manifest.format, 'aep-foundation-release-v1');
  assert.equal(manifest.version, version);
  assert.equal(manifest.commit, commit);
  assert.deepEqual(manifest.artifacts.map(item => item.file), [...expected].sort());
  for (const item of manifest.artifacts) {
    assert.match(item.sha256, /^[a-f0-9]{64}$/);
    assert.ok(item.bytes > 0);
  }
  const checksums = (await readFile(path.join(fixture, 'SHA256SUMS'), 'utf8')).trim().split(/\r?\n/);
  assert.equal(checksums.length, expected.length + 1);
  assert.ok(checksums.some(line => line.endsWith('  release-manifest.json')));

  await writeFile(path.join(fixture, 'unexpected.secret'), 'must be rejected');
  await assert.rejects(command(['--artifact-dir', fixture, '--version', version, '--commit', commit]), /unexpected release artifacts/);
  console.log('AEP release artifact manifest tests passed.');
} finally {
  await rm(fixture, {recursive: true, force: true});
}

function command(args) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [path.join(root, 'scripts', 'release-artifact-manifest.mjs'), ...args], {
      cwd: root,
      stdio: ['ignore', 'ignore', 'pipe'],
      shell: false,
    });
    const stderr = [];
    child.stderr.on('data', chunk => stderr.push(Buffer.from(chunk)));
    child.on('error', reject);
    child.on('exit', code => code === 0
      ? resolve()
      : reject(new Error(Buffer.concat(stderr).toString('utf8').trim() || `manifest command exited with ${code}`)));
  });
}

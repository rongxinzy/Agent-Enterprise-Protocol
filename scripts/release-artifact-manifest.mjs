import {createHash} from 'node:crypto';
import {readdir, readFile, writeFile} from 'node:fs/promises';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const options = parseArgs(process.argv.slice(2));
const artifactDirectory = path.resolve(root, options['artifact-dir'] ?? 'release/artifacts');
const packageDocument = JSON.parse(await readFile(path.join(root, 'package.json'), 'utf8'));
const version = options.version ?? packageDocument.version;
const commit = options.commit ?? process.env.GITHUB_SHA;
if (!commit || !/^[a-f0-9]{40}$/i.test(commit)) throw new Error('--commit must be a full Git commit SHA');

const expected = [
  `aep-offline-base-v${version}.tar.gz`,
  `aep-offline-gateway-v${version}.tar.gz`,
  'aep-foundation.source.sbom.cdx.json',
  'aep-control-service.image.sbom.cdx.json',
  'aep-gateway-authorizer.image.sbom.cdx.json',
  'aep-gateway-reconciler.image.sbom.cdx.json',
];
const actual = (await readdir(artifactDirectory, {withFileTypes: true}))
  .filter(entry => entry.isFile() && !['release-manifest.json', 'SHA256SUMS'].includes(entry.name))
  .map(entry => entry.name)
  .sort();
for (const file of expected) {
  if (!actual.includes(file)) throw new Error(`release artifact is missing: ${file}`);
}
const unexpected = actual.filter(file => !expected.includes(file));
if (unexpected.length > 0) throw new Error(`unexpected release artifacts: ${unexpected.join(', ')}`);

const artifacts = [];
for (const file of actual) {
  const content = await readFile(path.join(artifactDirectory, file));
  if (content.byteLength === 0) throw new Error(`release artifact is empty: ${file}`);
  if (file.endsWith('.sbom.cdx.json')) {
    const sbom = JSON.parse(content.toString('utf8'));
    if (sbom.bomFormat !== 'CycloneDX') throw new Error(`release SBOM is not CycloneDX: ${file}`);
  }
  if (file.endsWith('.tar.gz') && (content[0] !== 0x1f || content[1] !== 0x8b)) throw new Error(`release Bundle is not gzip encoded: ${file}`);
  artifacts.push({file, bytes: content.byteLength, sha256: digest(content)});
}
const manifest = {
  format: 'aep-foundation-release-v1',
  version,
  commit: commit.toLowerCase(),
  protocolVersion: '1.0',
  artifacts,
};
const manifestContent = Buffer.from(`${JSON.stringify(manifest, null, 2)}\n`);
await writeFile(path.join(artifactDirectory, 'release-manifest.json'), manifestContent);
const checksumEntries = [...artifacts, {file: 'release-manifest.json', sha256: digest(manifestContent)}];
await writeFile(path.join(artifactDirectory, 'SHA256SUMS'), `${checksumEntries.map(item => `${item.sha256}  ${item.file}`).join('\n')}\n`);
console.log(JSON.stringify({status: 'passed', artifactDirectory, version, commit: manifest.commit, artifacts: checksumEntries}, null, 2));

function digest(content) {
  return createHash('sha256').update(content).digest('hex');
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

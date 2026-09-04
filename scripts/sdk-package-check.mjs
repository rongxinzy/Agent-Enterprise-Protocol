import {createHash} from 'node:crypto';
import {spawnSync} from 'node:child_process';
import {
  copyFile,
  mkdir,
  mkdtemp,
  readFile,
  rm,
  writeFile,
} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const sdkPackage = JSON.parse(await readFile(path.join(root, 'packages', 'aep-sdk-node', 'package.json'), 'utf8'));
const outputDirectory = readOutputDirectory(process.argv.slice(2));
const temporaryRoot = await mkdtemp(path.join(os.tmpdir(), 'aep-sdk-package-'));

try {
  const first = await pack(path.join(temporaryRoot, 'pack-a'));
  const second = await pack(path.join(temporaryRoot, 'pack-b'));
  const firstHash = await sha256(first.archive);
  const secondHash = await sha256(second.archive);

  assert(first.filename === second.filename, 'repeated npm pack runs produced different filenames');
  assert(firstHash === secondHash, 'repeated npm pack runs produced different archives');
  assert(first.name === '@aep/sdk-node', 'unexpected SDK package name');
  assert(first.version === sdkPackage.version, 'SDK archive version does not match package.json');

  const packagedFiles = new Set(first.files.map(file => file.path));
  for (const required of ['README.md', 'README.zh-CN.md', 'dist/index.cjs', 'dist/index.d.ts', 'dist/index.js', 'package.json']) {
    assert(packagedFiles.has(required), `SDK package is missing ${required}`);
  }
  for (const file of packagedFiles) {
    assert(!file.startsWith('src/'), `SDK package leaks source file ${file}`);
    assert(!file.startsWith('test/'), `SDK package leaks test file ${file}`);
  }

  await verifyInstalledPackage(first.archive, path.join(temporaryRoot, 'consumer'));

  if (outputDirectory) {
    const resolvedOutput = path.resolve(root, outputDirectory);
    await mkdir(resolvedOutput, {recursive: true});
    await copyFile(first.archive, path.join(resolvedOutput, first.filename));
    await writeFile(
      path.join(resolvedOutput, `${first.filename}.sha256`),
      `${firstHash}  ${first.filename}\n`,
      'utf8',
    );
  }

  console.log(`AEP SDK package check passed: ${first.filename} sha256=${firstHash}`);
} finally {
  await rm(temporaryRoot, {recursive: true, force: true});
}

async function pack(destination) {
  await mkdir(destination, {recursive: true});
  const result = runNpm([
    'pack',
    '--workspace',
    '@aep/sdk-node',
    '--json',
    '--pack-destination',
    destination,
  ], root);
  const payload = JSON.parse(result.stdout);
  assert(Array.isArray(payload) && payload.length === 1, 'npm pack returned an unexpected result');
  const item = payload[0];
  assert(typeof item.filename === 'string', 'npm pack omitted the filename');
  assert(Array.isArray(item.files), 'npm pack omitted the file list');
  return {...item, archive: path.join(destination, item.filename)};
}

async function verifyInstalledPackage(archive, consumerDirectory) {
  await mkdir(consumerDirectory, {recursive: true});
  await writeFile(
    path.join(consumerDirectory, 'package.json'),
    JSON.stringify({name: 'aep-sdk-package-consumer', private: true, type: 'module'}, null, 2),
    'utf8',
  );
  runNpm(['install', '--ignore-scripts', '--no-audit', '--no-fund', archive], consumerDirectory);

  await writeFile(
    path.join(consumerDirectory, 'esm.mjs'),
    "import {AEP_PROTOCOL_VERSION, AepClient, ProtectedRefreshTokenStore} from '@aep/sdk-node';\n" +
      "if (AEP_PROTOCOL_VERSION !== '1.0' || !AepClient || !ProtectedRefreshTokenStore) process.exit(1);\n",
    'utf8',
  );
  await writeFile(
    path.join(consumerDirectory, 'commonjs.cjs'),
    "const {AEP_PROTOCOL_VERSION, AepClient, ProtectedRefreshTokenStore} = require('@aep/sdk-node');\n" +
      "if (AEP_PROTOCOL_VERSION !== '1.0' || !AepClient || !ProtectedRefreshTokenStore) process.exit(1);\n",
    'utf8',
  );
  await writeFile(
    path.join(consumerDirectory, 'types.ts'),
    "import {AepClient, ProtectedRefreshTokenStore, type AepProtectedStorage, type AepSessionState} from '@aep/sdk-node';\n" +
      'declare const storage: AepProtectedStorage;\n' +
      'const store = new ProtectedRefreshTokenStore(storage);\n' +
      "const client = new AepClient({baseUrl: 'https://aep.example', tokenStore: store});\n" +
      'const session: Promise<AepSessionState> = client.getSessionState();\n' +
      'void session;\n',
    'utf8',
  );
  await writeFile(
    path.join(consumerDirectory, 'tsconfig.json'),
    JSON.stringify({
      compilerOptions: {
        lib: ['ES2022', 'DOM', 'DOM.Iterable'],
        module: 'NodeNext',
        moduleResolution: 'NodeNext',
        noEmit: true,
        strict: true,
        types: [],
      },
      include: ['types.ts'],
    }, null, 2),
    'utf8',
  );

  run(process.execPath, ['esm.mjs'], consumerDirectory);
  run(process.execPath, ['commonjs.cjs'], consumerDirectory);
  run(process.execPath, [path.join(root, 'node_modules', 'typescript', 'bin', 'tsc'), '--project', 'tsconfig.json'], consumerDirectory);
}

function run(command, args, cwd) {
  const result = spawnSync(command, args, {cwd, encoding: 'utf8', shell: false});
  if (result.status !== 0) {
    throw new Error([
      `${command} ${args.join(' ')} failed with exit code ${result.status}`,
      result.stdout,
      result.stderr,
    ].filter(Boolean).join('\n'));
  }
  return result;
}

async function sha256(file) {
  return createHash('sha256').update(await readFile(file)).digest('hex');
}

function runNpm(args, cwd) {
  const npmExecutable = process.env.npm_execpath;
  assert(npmExecutable, 'npm_execpath is unavailable; run this check through npm');
  return run(process.execPath, [npmExecutable, ...args], cwd);
}

function readOutputDirectory(args) {
  if (args.length === 0) return null;
  if (args.length !== 2 || args[0] !== '--output-dir' || !args[1]) {
    throw new Error('usage: node scripts/sdk-package-check.mjs [--output-dir <directory>]');
  }
  return args[1];
}

function assert(condition, message) {
  if (!condition) throw new Error(`SDK package check failed: ${message}`);
}

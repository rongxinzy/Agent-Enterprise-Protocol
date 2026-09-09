import {randomBytes} from 'node:crypto';
import {spawn} from 'node:child_process';
import {copyFile, mkdtemp, readFile, rm, writeFile} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

import {AepClient, FetchTransport, MemoryTokenStore} from '../../packages/aep-sdk-node/dist/index.js';

if (process.env.CI === 'true' && process.env.AEP_ALLOW_OFFLINE_LICENSE_E2E !== '1') {
  throw new Error('Offline License E2E is local-only; run it outside CI to keep License material on the controlled network.');
}

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const baseComposeFile = path.join(root, 'deploy', 'compose', 'compose.yaml');
const offlineComposeFile = path.join(root, 'tests', 'e2e', 'offline-license.compose.yaml');
const fixtureDirectory = path.join(root, 'tests', 'e2e', 'fixtures');
const project = 'aep-offline-license-e2e';
const port = process.env.AEP_OFFLINE_E2E_PORT ?? '18089';
const baseUrl = `http://localhost:${port}`;
const adminPassword = `Offline-E2E-${randomBytes(18).toString('hex')}!`;
const signingSeed = randomBytes(32).toString('base64');
const composeEnv = {
  AEP_PORT: port,
  AEP_OFFLINE_E2E_PORT: port,
  AEP_OFFLINE_PORT: port,
  AEP_OFFLINE_FIXTURE_DIR: fixtureDirectory.replaceAll('\\', '/'),
  AEP_OFFLINE_PROXY_FILE: path.join(root, 'tests', 'e2e', 'offline-access-proxy.mjs').replaceAll('\\', '/'),
  AEP_OFFLINE_ADMIN_PASSWORD: adminPassword,
  AEP_OFFLINE_SIGNING_KEY_BASE64: signingSeed,
  AEP_MINIO_CONSOLE_PORT: process.env.AEP_OFFLINE_MINIO_CONSOLE_PORT ?? '19009',
};
const licenseText = await readFile(path.join(fixtureDirectory, 'offline-license.json'), 'utf8');
const trustedKeysText = await readFile(path.join(fixtureDirectory, 'offline-license-trusted-keys.json'), 'utf8');
const licenseEnvelope = JSON.parse(licenseText);
const trustedKeys = JSON.parse(trustedKeysText);
const licenseMaterial = [
  licenseText.trim(),
  trustedKeysText.trim(),
  licenseEnvelope.signature,
  trustedKeys[licenseEnvelope.keyId],
  JSON.stringify(licenseEnvelope.payload),
];
const temporaryRoot = await mkdtemp(path.join(root, '.offline-license-e2e-'));

class RecordingTransport {
  #delegate = new FetchTransport({maxRetries: 0});
  requests = [];

  async request(base, request) {
    this.requests.push({path: request.path, body: request.body, headers: request.headers});
    return this.#delegate.request(base, request);
  }
}

try {
  await compose('up', '-d', '--build');
  await waitForHealth();
  await assertInternalNetwork();
  await assertNoExternalEgress();
  await runActivationScenario();
  await assertLogsContainNoLicenseMaterial();
  await assertStartupRejectsTamperedLicense();
  await assertStartupRejectsDeploymentMismatch();
  console.log(JSON.stringify({
    status: 'passed',
    checks: [
      'production startup verifies a mounted License with a public key only',
      'Control Service network is internal and has no external egress',
      'activation request contains no License or signing material',
      'activation response contains only entitlement metadata',
      'activation remains valid after service restart',
      'tampered License is rejected before service startup',
      'deployment-mismatched License is rejected before service startup',
      'service logs contain no License envelope or key material',
    ],
  }));
} finally {
  await compose('down', '-v', '--remove-orphans', true);
  await rm(temporaryRoot, {recursive: true, force: true});
}

async function runActivationScenario() {
  const transport = new RecordingTransport();
  const client = new AepClient({baseUrl, tokenStore: new MemoryTokenStore(), transport});
  await client.loginWithPassword({
    deploymentId: 'offline-e2e-deployment',
    username: 'admin',
    password: adminPassword,
  });

  const first = await client.activateEnterpriseLicense();
  assert(first.licenseId === 'lic-offline-e2e-v1', 'activation returned the wrong License');
  assert(first.deploymentId === 'offline-e2e-deployment', 'activation returned the wrong deployment');
  assert(first.features.includes('enterprise.models'), 'activation omitted the licensed feature');
  assertNoLicenseMaterial(JSON.stringify(first), 'activation response exposed License material');
  const activationRequests = transport.requests.filter(item => item.path === '/aep/v1/user/activation');
  assert(activationRequests.length === 1, 'activation did not make exactly one request');
  assert(JSON.stringify(activationRequests[0].body) === '{}', 'activation uploaded License evidence');
  assertNoLicenseMaterial(JSON.stringify(activationRequests[0]), 'activation request exposed License material');

  await compose('restart', 'control-service');
  await waitForHealth();
  const second = await client.activateEnterpriseLicense();
  assert(second.licenseId === first.licenseId && second.licenseDigest === first.licenseDigest, 'activation did not survive service restart');
  assert(transport.requests.filter(item => item.path === '/aep/v1/user/activation').length === 2, 'restart activation was not observed');
}

async function assertInternalNetwork() {
  const containerId = await commandOutput('docker', ['compose', ...composeArgs(), 'ps', '-q', 'control-service'], composeEnv);
  const inspect = JSON.parse(await commandOutput('docker', ['inspect', containerId]))[0];
  const runtimeEnvironment = inspect?.Config?.Env ?? [];
  assert(!runtimeEnvironment.some(value => /LICENSE[_-](?:PRIVATE|SIGNER)|SIGNER[_-]PRIVATE/i.test(value)), 'Control Service received License signer material');
  assert(!(inspect?.Mounts ?? []).some(mount => /license[_-]signer|private[_-]?key/i.test(`${mount.Source} ${mount.Destination}`)), 'Control Service mounted a signer or private-key path');
  const networks = JSON.parse(await commandOutput('docker', ['inspect', '--format', '{{json .NetworkSettings.Networks}}', containerId]));
  const names = Object.keys(networks);
  assert(names.length === 1, 'Control Service is attached to an unexpected external network');
  const network = JSON.parse(await commandOutput('docker', ['network', 'inspect', names[0]]));
  assert(network.length === 1 && network[0].Internal === true, 'Control Service network is not internal');
}

async function assertNoExternalEgress() {
  await commandOutput('docker', ['compose', '--profile', 'offline-probe', ...composeArgs(), 'run', '--rm', '--no-deps', '-T', 'offline-egress-probe'], composeEnv);
}

async function assertLogsContainNoLicenseMaterial() {
  const logs = await commandOutput('docker', ['compose', ...composeArgs(), 'logs', '--no-color', '--no-log-prefix', 'control-service'], composeEnv);
  for (const forbidden of [licenseText.trim(), trustedKeysText.trim(), 'BEGIN PRIVATE KEY', 'BEGIN OPENSSH PRIVATE KEY', 'license-signer']) {
    assert(!logs.includes(forbidden), 'Control Service logs exposed sensitive License material');
  }
  assertNoLicenseMaterial(logs, 'Control Service logs exposed License material');
}

async function assertStartupRejectsTamperedLicense() {
  const directory = await makeVariantDirectory('tampered');
  const output = await runExpectedFailure(directory);
  assertNoLicenseMaterial(output, 'tampered License failure exposed License material');
}

async function assertStartupRejectsDeploymentMismatch() {
  const output = await runExpectedFailure(fixtureDirectory, {AEP_OFFLINE_LICENSE_DEPLOYMENT_ID: 'another-deployment'});
  assertNoLicenseMaterial(output, 'deployment mismatch exposed License material');
}

async function makeVariantDirectory(name) {
  const directory = await mkdtemp(path.join(temporaryRoot, `${name}-`));
  const envelope = JSON.parse(licenseText);
  envelope.payload.features = ['enterprise.models', 'tampered'];
  await writeFile(path.join(directory, 'offline-license.json'), JSON.stringify(envelope), 'utf8');
  await copyFile(path.join(fixtureDirectory, 'offline-license-trusted-keys.json'), path.join(directory, 'offline-license-trusted-keys.json'));
  return directory;
}

async function runExpectedFailure(directory, overrides = {}) {
  const output = await commandOutput(
    'docker',
    ['compose', ...composeArgs(), 'run', '--rm', '--no-deps', '-T', 'control-service'],
    {...composeEnv, AEP_OFFLINE_FIXTURE_DIR: directory.replaceAll('\\', '/'), ...overrides},
    {expectFailure: true, timeoutMs: 30_000},
  );
  return output;
}

async function waitForHealth() {
  const deadline = Date.now() + 120_000;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(`${baseUrl}/readyz`);
      if (response.ok) return;
    } catch {}
    await new Promise(resolve => setTimeout(resolve, 1_000));
  }
  throw new Error('Offline License Control Service did not become healthy within 120 seconds');
}

function composeArgs() {
  return ['-p', project, '-f', baseComposeFile, '-f', offlineComposeFile];
}

function compose(...args) {
  let allowFailure = false;
  if (args.at(-1) === true) {
    allowFailure = true;
    args.pop();
  }
  return command('docker', ['compose', ...composeArgs(), ...args], composeEnv, {allowFailure});
}

function command(executable, args, extraEnv = {}, options = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {
      cwd: root,
      env: {...process.env, ...extraEnv},
      stdio: options.capture === false ? 'inherit' : ['ignore', 'pipe', 'pipe'],
      shell: false,
    });
    const stdout = [];
    const stderr = [];
    if (options.capture !== false) {
      child.stdout.on('data', chunk => stdout.push(Buffer.from(chunk)));
      child.stderr.on('data', chunk => stderr.push(Buffer.from(chunk)));
    }
    const timer = options.timeoutMs ? setTimeout(() => child.kill(), options.timeoutMs) : null;
    child.on('error', error => {
      if (timer) clearTimeout(timer);
      reject(error);
    });
    child.on('exit', code => {
      if (timer) clearTimeout(timer);
      const output = Buffer.concat([...stdout, ...stderr]).toString('utf8');
      if (options.expectFailure ? code && code !== 0 : code === 0 || options.allowFailure) {
        resolve(output.trim());
      } else {
        reject(new Error(`${executable} ${args.join(' ')} exited with ${code ?? 'signal'}`));
      }
    });
  });
}

function commandOutput(executable, args, extraEnv = {}, options = {}) {
  return command(executable, args, extraEnv, options);
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function assertNoLicenseMaterial(value, message) {
  for (const forbidden of licenseMaterial) {
    if (forbidden && value.includes(forbidden)) throw new Error(message);
  }
}

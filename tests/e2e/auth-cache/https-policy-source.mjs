// Optional integration with the separately reviewed native HTTPS file source.
// Inputs are deployment paths and disposable PKI, never customer credentials.
import assert from 'node:assert/strict';
import {readFile, writeFile, rm} from 'node:fs/promises';
import path from 'node:path';
import {image} from './resources.mjs';

export async function httpsPolicySource({directory, command, kubectl, apply, work, cluster, namespace}) {
  const host = 'aep-access-policy-source.' + namespace + '.svc';
  const file = name => path.join(work, name);
  const openssl = process.env.OPENSSL ?? 'openssl';
  await command(openssl, ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', file('ca.key'),
    '-out', file('ca.crt'), '-days', '1', '-subj', '/CN=disposable-native-access-ca']);
  await command(openssl, ['req', '-newkey', 'rsa:2048', '-nodes', '-keyout', file('server.key'),
    '-out', file('server.csr'), '-subj', '/CN=' + host]);
  await writeFile(file('server.ext'), 'subjectAltName=DNS:' + host + '\nbasicConstraints=CA:FALSE\n' +
    'keyUsage=digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\n');
  await command(openssl, ['x509', '-req', '-in', file('server.csr'), '-CA', file('ca.crt'),
    '-CAkey', file('ca.key'), '-CAcreateserial', '-out', file('server.crt'), '-days', '1', '-extfile', file('server.ext')]);
  const manifest = await command('kubectl', ['kustomize', directory]);
  const refs = [...manifest.matchAll(/^\s+image: (\S+)/gm)].map(match => match[1]);
  assert.equal(refs.length, 2);
  for (const [index, ref] of refs.entries()) {
    assert.match(ref, /@sha256:[a-f0-9]{64}$/);
    const tag = ref.split('@')[0], digest = ref.split('@')[1];
    const inspection = JSON.parse(await command('docker', ['image', 'inspect', tag]));
    assert(inspection[0].RepoDigests.some(value => value.endsWith('@' + digest)));
    const archive = file('policy-source-' + index + '.tar');
    await command('docker', ['image', 'save', '-o', archive, tag]);
    await command('docker', ['cp', archive, cluster + '-control-plane:/aep-policy-source.tar']);
    await command('docker', ['exec', cluster + '-control-plane', 'ctr', '-n', 'k8s.io', 'images', 'import',
      '--digests', '--platform', 'linux/amd64', '/aep-policy-source.tar']);
    await command('docker', ['exec', cluster + '-control-plane', 'rm', '--', '/aep-policy-source.tar']);
    await rm(archive);
  }
  const secret = await kubectl(['-n', namespace, 'create', 'secret', 'tls', 'aep-access-policy-tls',
    '--cert=' + file('server.crt'), '--key=' + file('server.key'), '--dry-run=client', '-o', 'json']);
  await kubectl(['apply', '-f', '-'], {input: secret});
  // Preserve the pinned verifier image's public roots, needed by real issuers.
  const roots = await command('docker', ['run', '--rm', '--entrypoint', '/bin/cat', image, '/etc/pki/tls/certs/ca-bundle.crt']);
  assert(roots.includes('-----BEGIN CERTIFICATE-----'));
  const bundle = roots + '\n' + await readFile(file('ca.crt'), 'utf8');
  await apply([{apiVersion: 'v1', kind: 'ConfigMap', metadata: {name: 'native-policy-ca', namespace}, data: {'ca-bundle.crt': bundle}}]);
  await kubectl(['apply', '-f', '-'], {input: manifest});
  await kubectl(['-n', namespace, 'rollout', 'status', 'deployment/aep-access-policy-source', '--timeout=150s']);
  return {
    baseURL: 'https://' + host,
    configure(objects) {
      const valid = objects.filter(o => o.kind === 'Deployment' && o.metadata.name.startsWith('authorino-'));
      // One cold negative control has the same RBAC/configuration but no private CA.
      const untrusted = structuredClone(valid[0]);
      untrusted.metadata.name = 'authorino-no-ca';
      untrusted.spec.selector.matchLabels.app = 'authorino-no-ca';
      untrusted.spec.template.metadata.labels.app = 'authorino-no-ca';
      for (const deployment of valid) {
        const pod = deployment.spec.template;
        pod.metadata.labels['gateway.aep.zhiyuan.io/policy-reader'] = 'true';
        pod.spec.volumes = [{name: 'policy-ca', configMap: {name: 'native-policy-ca'}}];
        pod.spec.containers[0].env = [{name: 'SSL_CERT_FILE', value: '/etc/aep-ca/ca-bundle.crt'}];
        pod.spec.containers[0].volumeMounts = [{name: 'policy-ca', mountPath: '/etc/aep-ca', readOnly: true}];
      }
      untrusted.spec.template.metadata.labels['gateway.aep.zhiyuan.io/policy-reader'] = 'true';
      objects.push(untrusted);
    },
  };
}

export async function sourceFailureChecks({kubectl, apply, get, grant, spec, name, ports, binding, token, request, until, record}) {
  const namespace = 'aep-system';
  const call = port => request(port, binding + '.access.aep.invalid', token);
  const rv = (await get('authconfig', name)).metadata.resourceVersion;
  spec.publication.sequence++; spec.publication.revision = 'https-short-publication';
  spec.validUntil = new Date(Date.now() + 35_000).toISOString();
  await apply([grant()]);
  for (const port of ports) await until(async () => {
    const result = await call(port);
    assert.equal(result.status, 200); assert.equal(result.observation.publication.sequence, spec.publication.sequence);
  });
  const deadline = Date.parse(spec.validUntil);
  await kubectl(['-n', namespace, 'scale', 'deployment/aep-access-policy-source', '--replicas=0']);
  await until(async () => {
    const pods = JSON.parse(await kubectl(['-n', namespace, 'get', 'pods', '-l', 'app.kubernetes.io/name=aep-access-policy-source', '-o', 'json']));
    assert.equal(pods.items.length, 0);
  });
  // Surviving native modules may be used only until their published deadline.
  assert(Date.now() < deadline - 2000, 'Source interruption setup exceeded the short publication lifetime');
  for (const port of ports) assert.equal((await call(port)).status, 200);
  await until(async () => assert(Date.now() > deadline + 100));
  for (const port of ports) assert.equal((await call(port)).status, 403);
  spec.publication.sequence++; spec.publication.revision = 'https-offline-renewal';
  spec.validUntil = new Date(Date.now() + 180_000).toISOString();
  await apply([grant()]);
  await until(async () => assert.equal((await get('configmap', name)).metadata.annotations['gateway.aep.zhiyuan.io/publication-sequence'],
    String(spec.publication.sequence)));
  for (const port of ports) assert.equal((await call(port)).status, 403);
  record('HTTPS source outage keeps the last module only until its deadline; offline CR renewal cannot extend it');
  await kubectl(['-n', namespace, 'scale', 'deployment/aep-access-policy-source', '--replicas=2']);
  await kubectl(['-n', namespace, 'rollout', 'status', 'deployment/aep-access-policy-source', '--timeout=150s']);
  for (const port of ports) await until(async () => {
    const result = await call(port);
    assert.equal(result.status, 200); assert.equal(result.observation.publication.sequence, spec.publication.sequence);
  });
  assert.equal((await get('authconfig', name)).metadata.resourceVersion, rv);
  record('Both native processes recover the current HTTPS publication without AuthConfig writes');
}

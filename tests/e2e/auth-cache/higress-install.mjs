// Real pinned Higress for the opt-in access acceptance, never a product overlay.
import {readFile, writeFile, rm} from 'node:fs/promises';
import path from 'node:path';

export async function installAccessHigress({command, kubectl, work, cluster}) {
  console.log('Installing real Higress 2.2.4 for shared access acceptance');
  for (const [i, component] of ['gateway', 'higress', 'pilot'].entries()) {
    const image = 'higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/' + component + ':2.2.4';
    const archive = path.join(work, 'higress-' + i + '.tar');
    await command('docker', ['image', 'save', '--platform=linux/amd64', '-o', archive, image]);
    await command(process.env.KIND ?? 'kind', ['load', 'image-archive', '--name', cluster, archive]);
    await rm(archive);
  }
  const helm = process.env.HELM ?? 'helm';
  let chart = process.env.AEP_HIGRESS_CORE_CHART;
  if (!chart) {
    await command(helm, ['pull', 'higress-core', '--repo', 'https://higress.io/helm-charts', '--version', '2.2.4', '--untar', '--untardir', work]);
    chart = path.join(work, 'higress-core');
  }
  const chartMetadata = await readFile(path.join(chart, 'Chart.yaml'), 'utf8');
  if (!/^version: ["']?2\.2\.4["']?\s*$/m.test(chartMetadata)) throw new Error('Expected pinned Higress chart 2.2.4');
  const values = path.join(work, 'higress-values.json');
  await writeFile(values, JSON.stringify({global: {local: true, enableIstioAPI: true, enableGatewayAPI: false, o11y: {enabled: false}},
    gateway: {replicas: 1, service: {type: 'ClusterIP'}, resources: {requests: {cpu: '100m', memory: '256Mi'}, limits: {memory: '1Gi'}}},
    controller: {replicas: 1, automaticHttps: {enabled: false}, resources: {requests: {cpu: '100m', memory: '128Mi'}}}}));
  const crds = await command(helm, ['show', 'crds', chart]);
  if (crds.trim()) await kubectl(['apply', '-f', '-'], {input: crds});
  await kubectl(['wait', '--for=condition=Established', 'crds', '--all', '--timeout=60s']);
  const manifest = await command(helm, ['template', 'higress', chart, '--namespace', 'higress-system', '--include-crds', '-f', values]);
  await kubectl(['apply', '-f', '-'], {input: manifest});
  for (const name of ['higress-controller', 'higress-gateway']) {
    await kubectl(['-n', 'higress-system', 'rollout', 'status', 'deployment/' + name, '--timeout=180s']);
  }
}

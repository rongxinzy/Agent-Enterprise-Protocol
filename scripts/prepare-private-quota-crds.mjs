// Select real, version-pinned upstream schemas for the opt-in local probe.
import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {readFile, writeFile} from 'node:fs/promises';
import yaml from 'js-yaml';

const [source, destination] = process.argv.slice(2);
assert(source && destination, 'Usage: node scripts/prepare-private-quota-crds.mjs <Istio-1.27.0-crd-all.gen.yaml> <output.json>');
const raw = await readFile(source, 'utf8');
assert.equal(createHash('sha256').update(raw).digest('hex'),
  '88ad815127f8d400fab07404630bc2cb2b2a8934e3396eea5972295bd36c354b', 'Unexpected upstream Istio CRD bundle');
const names = new Set(['gateways.networking.istio.io', 'virtualservices.networking.istio.io', 'authorizationpolicies.security.istio.io']);
const items = yaml.loadAll(raw).filter(item => names.has(item?.metadata?.name));
assert.equal(items.length, names.size);
assert(items.every(item => item.kind === 'CustomResourceDefinition' && item.spec.versions.some(v => v.name === 'v1' && v.served)));
const body = JSON.stringify({apiVersion: 'v1', kind: 'List', items}, null, 2);
assert.equal(createHash('sha256').update(body).digest('hex'),
  '7544518ef89715cc11c9f07d4f52cba5656b323ee4eace898f69448782ab96e4');
await writeFile(destination, body);
console.log('Prepared three native Istio CRDs for isolated quota acceptance.');

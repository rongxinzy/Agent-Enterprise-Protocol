import {createHash} from 'node:crypto';
import {mkdir, readFile, writeFile} from 'node:fs/promises';
import path from 'node:path';
import {parseArgs} from 'node:util';

// Official OSS Higress Console v2.2.4 dashboard. Keep its panels and formulas;
// adapt only the existing datasource and the gateway scope for shared servers.
const sourceUrl = 'https://raw.githubusercontent.com/higress-group/higress-console/f8410432d450541baec3ae468e4d4a8a07392169/backend/console/src/main/resources/dashboard/ai.json';
const sourceSha256 = '1099be8b889f2853908b15bac75df5caedeafb3d2d34298bf758a4629ca30484';
const {values} = parseArgs({options: {
  'datasource-uid': {type: 'string'},
  output: {type: 'string'},
  template: {type: 'string'},
}});
if (!/^[A-Za-z0-9_-]{1,40}$/.test(values['datasource-uid'] ?? '') || !values.output) {
  throw new Error('Usage: node scripts/render-higress-dashboard.mjs --datasource-uid <existing-uid> --output <file.json> [--template <cached-official-ai.json>]');
}

let source;
if (values.template) {
  source = await readFile(values.template);
} else {
  const response = await fetch(sourceUrl, {signal: AbortSignal.timeout(30_000)});
  if (!response.ok) throw new Error(`Official dashboard download failed: HTTP ${response.status}`);
  source = Buffer.from(await response.arrayBuffer());
}
if (createHash('sha256').update(source).digest('hex') !== sourceSha256) {
  throw new Error('Official dashboard checksum mismatch; no output written');
}
const dashboard = JSON.parse(source.toString('utf8'));
let datasources = 0;
let scopedQueries = 0;
function adapt(value) {
  if (!value || typeof value !== 'object') return;
  if (value.type === 'prometheus' && value.uid === '${datasource.id}') {
    value.uid = values['datasource-uid'];
    datasources++;
  }
  if (typeof value.expr === 'string') {
    value.expr = value.expr.replace(/\b(route_upstream_model_consumer_metric_[A-Za-z0-9_]+)(?:\{\})?(?=\[)/g, (_match, metric) => {
      scopedQueries++;
      return `${metric}{higress="$gateway"}`;
    });
  }
  for (const child of Object.values(value)) adapt(child);
}
adapt(dashboard);
if (!datasources || !scopedQueries) throw new Error('Official dashboard structure was not recognized; no output written');
dashboard.uid = 'aep-higress-ai';
dashboard.title = 'AEP Higress AI Gateway';
const output = path.resolve(values.output);
await mkdir(path.dirname(output), {recursive: true});
await writeFile(output, JSON.stringify(dashboard, null, 2) + '\n', 'utf8');
console.log(`Rendered official Higress dashboard: ${output} (${datasources} datasource references, ${scopedQueries} gateway selectors). Import into the existing Grafana; no server was changed.`);

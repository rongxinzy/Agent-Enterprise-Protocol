const baseUrl = process.env.AEP_LOAD_BASE_URL ?? 'http://localhost:8080';
const durationSeconds = positiveNumber('AEP_LOAD_DURATION_SECONDS', 10);
const concurrency = positiveNumber('AEP_LOAD_CONCURRENCY', 8);
const timeoutMs = positiveNumber('AEP_LOAD_TIMEOUT_MS', 5_000);
const maxErrorRate = Number(process.env.AEP_LOAD_MAX_ERROR_RATE ?? '0.01');
const endpoint = process.env.AEP_LOAD_ENDPOINT ?? '/healthz';

if (!Number.isFinite(maxErrorRate) || maxErrorRate < 0 || maxErrorRate > 1) {
  throw new Error('AEP_LOAD_MAX_ERROR_RATE must be a number between 0 and 1');
}

const target = new URL(endpoint, baseUrl).toString();
const deadline = Date.now() + durationSeconds * 1_000;
const latencies = [];
let requests = 0;
let failures = 0;

async function worker() {
  while (Date.now() < deadline) {
    const started = performance.now();
    let ok = false;
    try {
      const response = await fetch(target, {signal: AbortSignal.timeout(timeoutMs)});
      ok = response.ok;
      await response.arrayBuffer();
    } catch {}
    requests += 1;
    if (!ok) failures += 1;
    latencies.push(performance.now() - started);
  }
}

await Promise.all(Array.from({length: concurrency}, () => worker()));

if (requests === 0) throw new Error('load test completed without requests');
latencies.sort((left, right) => left - right);
const p95 = percentile(latencies, 0.95);
const errorRate = failures / requests;
const summary = {
  target,
  durationSeconds,
  concurrency,
  requests,
  failures,
  errorRate: Number(errorRate.toFixed(4)),
  p95Ms: Number(p95.toFixed(2)),
};
console.log(JSON.stringify(summary, null, 2));
if (errorRate > maxErrorRate) {
  throw new Error(`error rate ${errorRate.toFixed(4)} exceeded ${maxErrorRate.toFixed(4)}`);
}

function percentile(values, rank) {
  const index = Math.min(values.length - 1, Math.ceil(values.length * rank) - 1);
  return values[index];
}

function positiveNumber(name, fallback) {
  const value = Number(process.env[name] ?? fallback);
  if (!Number.isFinite(value) || value <= 0) throw new Error(`${name} must be a positive number`);
  return value;
}

// Disposable RFC 7662 fixture, not an AEP endpoint or production verifier.
import http from 'node:http';

const calls = {};
const expirations = {};
let jwks = {keys: []};
const subjects = {
  'probe-alice-session': ['alice', 'session-a'],
  'probe-bob-session': ['bob', 'session-b'],
  'probe-short-session': ['alice', 'session-short'],
  'probe-guarded-session': ['alice', 'session-guarded'],
};
http.createServer(async (req, res) => {
  res.setHeader('content-type', 'application/json');
  if (req.url === '/stats') return res.end(JSON.stringify({calls, expirations}));
  if (req.url === '/jwks' && req.method === 'POST') {
    let body = '';
    for await (const chunk of req) body += chunk;
    jwks = JSON.parse(body); // Public keys only; disposable private keys never enter the cluster.
    return res.end('{}');
  }
  if (req.url === '/jwks') return res.end(JSON.stringify(jwks));
  if (req.url === '/oidc/.well-known/openid-configuration') return res.end(JSON.stringify({
    issuer: 'http://introspection:8080/oidc', jwks_uri: 'http://introspection:8080/jwks',
    id_token_signing_alg_values_supported: ['EdDSA'],
  }));
  if (req.url === '/echo' && req.method === 'POST') {
    for await (const chunk of req) void chunk;
    return res.end(JSON.stringify({witness: req.headers['x-probe-witness']}));
  }
  if (req.url !== '/introspect' || req.method !== 'POST') {
    res.writeHead(404).end('{}');
    return;
  }
  let body = '';
  for await (const chunk of req) body += chunk;
  const token = new URLSearchParams(body).get('token');
  const entry = subjects[token];
  if (!entry) return res.end(JSON.stringify({active: false}));
  const [sub, sid] = entry;
  const now = Math.floor(Date.now() / 1000);
  expirations[sid] ??= now + (sid === 'session-short' || sid === 'session-guarded' ? 4 : 600);
  calls[sid] = (calls[sid] ?? 0) + 1;
  res.end(JSON.stringify({active: now < expirations[sid], sub, sid,
    deployment_id: 'probe', model_scopes: ['model-a', 'model-b'],
    exp: expirations[sid], serial: calls[sid]}));
}).listen(8080, '0.0.0.0');

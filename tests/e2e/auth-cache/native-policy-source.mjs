// Disposable static-file source appended to the existing JWT fixture module.
// It reads Kubernetes-projected ConfigMap files; never deploy this test server.
import {createServer as createPolicyServer} from 'node:http';
import {readFile as readPolicyFile} from 'node:fs/promises';

createPolicyServer(async (req, res) => {
  if (req.method !== 'GET' || !/^\/access-[a-f0-9]{64}\.rego$/.test(req.url)) return res.writeHead(404).end();
  try {
    const policy = await readPolicyFile('/policies' + req.url, 'utf8');
    res.writeHead(200, {'content-type': 'text/plain'});
    res.end(policy);
  } catch {
    res.writeHead(503).end('unavailable');
  }
}).listen(8082, '0.0.0.0');

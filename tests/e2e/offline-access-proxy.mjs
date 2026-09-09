import {createServer} from 'node:http';

const port = Number(process.env.PROXY_PORT ?? 8080);
const targetHost = process.env.CONTROL_HOST ?? 'control-service';
const targetPort = Number(process.env.CONTROL_PORT ?? 8080);

createServer(async (request, response) => {
  try {
    const body = await readBody(request);
    const upstream = await fetch(`http://${targetHost}:${targetPort}${request.url}`, {
      method: request.method,
      headers: {...request.headers, host: `${targetHost}:${targetPort}`},
      body: body.length === 0 || request.method === 'GET' || request.method === 'HEAD' ? undefined : body,
    });
    response.statusCode = upstream.status;
    upstream.headers.forEach((value, key) => {
      if (key !== 'transfer-encoding' && key !== 'connection') response.setHeader(key, value);
    });
    response.end(Buffer.from(await upstream.arrayBuffer()));
  } catch {
    response.statusCode = 502;
    response.end('offline access proxy unavailable');
  }
}).listen(port, '0.0.0.0');

function readBody(request) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    request.on('data', chunk => chunks.push(Buffer.from(chunk)));
    request.on('end', () => resolve(Buffer.concat(chunks)));
    request.on('error', reject);
  });
}

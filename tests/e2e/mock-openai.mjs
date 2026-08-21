import http from 'node:http';

const port = Number(process.env.MOCK_OPENAI_PORT ?? '8080');
const apiKey = process.env.MOCK_OPENAI_API_KEY ?? 'm1-e2e-provider-secret';
const expectedModel = process.env.MOCK_OPENAI_MODEL ?? 'mock-upstream-chat';

const server = http.createServer(async (request, response) => {
  if (request.method === 'GET' && request.url === '/healthz') {
    sendJSON(response, 200, {status: 'ok'});
    return;
  }
  if (request.method !== 'POST' || request.url !== '/v1/chat/completions') {
    sendJSON(response, 404, {error: {message: 'route not found'}});
    return;
  }
  if (request.headers.authorization !== `Bearer ${apiKey}`) {
    sendJSON(response, 401, {error: {message: 'provider credential was not injected'}});
    return;
  }
  const body = await readJSON(request);
  if (body.model !== expectedModel) {
    sendJSON(response, 400, {error: {message: `expected rewritten model ${expectedModel}`}});
    return;
  }
  if (request.headers['x-aep-tenant-id'] !== 'demo' || !request.headers['x-aep-user-id'] || !request.headers['x-aep-agent-id'] || request.headers['x-aep-model-id'] !== 'enterprise-chat') {
    sendJSON(response, 400, {error: {message: 'trusted AEP identity headers are missing'}});
    return;
  }
  if (body.messages?.[0]?.content === 'force upstream failure') {
    sendJSON(response, 503, {error: {message: 'forced upstream failure', type: 'upstream_error'}});
    return;
  }
  response.setHeader('X-Mock-Provider-Auth', 'accepted');
  if (body.stream === true) {
    response.writeHead(200, {'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache'});
    response.write(`data: ${JSON.stringify(chunk('Hello'))}\n\n`);
    setTimeout(() => {
      response.write(`data: ${JSON.stringify(chunk(' AEP'))}\n\n`);
      response.end('data: [DONE]\n\n');
    }, 40);
    return;
  }
  sendJSON(response, 200, {
    id: 'chatcmpl-aep-m1', object: 'chat.completion', created: 1, model: expectedModel,
    choices: [{index: 0, message: {role: 'assistant', content: 'Hello AEP'}, finish_reason: 'stop'}],
    usage: {prompt_tokens: 1, completion_tokens: 2, total_tokens: 3},
  });
});

server.listen(port, '0.0.0.0');

function chunk(content) {
  return {id: 'chatcmpl-aep-m1', object: 'chat.completion.chunk', created: 1, model: expectedModel, choices: [{index: 0, delta: {content}, finish_reason: null}]};
}

function sendJSON(response, status, value) {
  response.writeHead(status, {'Content-Type': 'application/json'});
  response.end(JSON.stringify(value));
}

async function readJSON(request) {
  const chunks = [];
  for await (const chunk of request) chunks.push(Buffer.from(chunk));
  return JSON.parse(Buffer.concat(chunks).toString('utf8'));
}

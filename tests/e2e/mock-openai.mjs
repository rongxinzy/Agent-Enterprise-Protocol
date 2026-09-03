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
  if (body.messages?.[0]?.content === 'verify reasoning replay') {
    const assistant = body.messages?.find(message => message.role === 'assistant');
    if (body.thinking?.type !== 'enabled' || body.reasoning_effort !== 'high' || assistant?.reasoning_content !== 'Call the clock tool.') {
      sendJSON(response, 400, {error: {message: 'DeepSeek thinking parameters or assistant reasoning replay were lost'}});
      return;
    }
  }
  const lastUserMessage = [...(body.messages ?? [])]
    .reverse()
    .find(message => message.role === 'user');
  const lastUserText = messageText(lastUserMessage?.content);
  if (lastUserText.includes('AEP_TOOL_CONTINUATION')) {
    const toolResult = body.messages?.find(
      message => message.role === 'tool' && message.tool_call_id === 'call-aep-electron',
    );
    if (toolResult) {
      const assistant = body.messages?.find(message =>
        message.role === 'assistant'
        && message.tool_calls?.some(toolCall => toolCall.id === 'call-aep-electron'),
      );
      if (assistant?.reasoning_content !== 'Use the read-only conversation history tool.') {
        sendJSON(response, 400, {error: {message: 'assistant reasoning content was lost before tool continuation'}});
        return;
      }
      streamCompletion(response, 'AEP_TOOL_CONTINUATION_OK', 'Tool result received.');
      return;
    }
    const availableTools = (body.tools ?? []).map(tool => tool?.function?.name).filter(Boolean);
    if (availableTools.includes('conversation_history')) {
      streamToolCall(response);
      return;
    }
  }
  if (lastUserText.includes('AEP_CANCEL_SLOW')) {
    streamSlowCompletion(request, response);
    return;
  }
  response.setHeader('X-Mock-Provider-Auth', 'accepted');
  if (body.stream === true) {
    response.writeHead(200, {'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache'});
    response.write(`data: ${JSON.stringify(chunk('', 'Think through the request.'))}\n\n`);
    response.write(`data: ${JSON.stringify(chunk('Hello'))}\n\n`);
    setTimeout(() => {
      response.write(`data: ${JSON.stringify(chunk(' AEP', undefined, 'stop'))}\n\n`);
      response.end('data: [DONE]\n\n');
    }, 40);
    return;
  }
  sendJSON(response, 200, {
    id: 'chatcmpl-aep-m1', object: 'chat.completion', created: 1, model: expectedModel,
    choices: [{index: 0, message: {role: 'assistant', content: 'Hello AEP', reasoning_content: 'Think through the request.'}, finish_reason: 'stop'}],
    usage: {prompt_tokens: 1, completion_tokens: 2, total_tokens: 3},
  });
});

server.listen(port, '0.0.0.0');

function chunk(content, reasoningContent, finishReason = null) {
  return {id: 'chatcmpl-aep-m1', object: 'chat.completion.chunk', created: 1, model: expectedModel, choices: [{index: 0, delta: {...(content ? {content} : {}), ...(reasoningContent ? {reasoning_content: reasoningContent} : {})}, finish_reason: finishReason}]};
}

function messageText(content) {
  if (typeof content === 'string') return content;
  if (!Array.isArray(content)) return '';
  return content
    .map(part => typeof part === 'string' ? part : part?.type === 'text' ? part.text ?? '' : '')
    .join('');
}

function streamCompletion(response, content, reasoningContent) {
  response.setHeader('X-Mock-Provider-Auth', 'accepted');
  response.writeHead(200, {'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache'});
  response.write(`data: ${JSON.stringify(chunk('', reasoningContent))}\n\n`);
  response.write(`data: ${JSON.stringify(chunk(content, undefined, 'stop'))}\n\n`);
  response.end('data: [DONE]\n\n');
}

function streamToolCall(response) {
  response.setHeader('X-Mock-Provider-Auth', 'accepted');
  response.writeHead(200, {'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache'});
  response.write(`data: ${JSON.stringify(chunk('', 'Use the read-only conversation history tool.'))}\n\n`);
  response.write(`data: ${JSON.stringify({
    id: 'chatcmpl-aep-m1', object: 'chat.completion.chunk', created: 1, model: expectedModel,
    choices: [{index: 0, delta: {tool_calls: [{
      index: 0, id: 'call-aep-electron', type: 'function',
      function: {name: 'conversation_history', arguments: '{"query":"AEP_TOOL_CONTINUATION","limit":1}'},
    }]}, finish_reason: null}],
  })}\n\n`);
  response.write(`data: ${JSON.stringify(chunk('', undefined, 'tool_calls'))}\n\n`);
  response.end('data: [DONE]\n\n');
}

function streamSlowCompletion(_request, response) {
  response.setHeader('X-Mock-Provider-Auth', 'accepted');
  response.writeHead(200, {'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache'});
  response.write(`data: ${JSON.stringify(chunk('', 'Waiting for cancellation.'))}\n\n`);
  const timer = setTimeout(() => {
    response.write(`data: ${JSON.stringify(chunk('AEP_CANCEL_NOT_ABORTED', undefined, 'stop'))}\n\n`);
    response.end('data: [DONE]\n\n');
  }, 10_000);
  response.once('close', () => clearTimeout(timer));
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

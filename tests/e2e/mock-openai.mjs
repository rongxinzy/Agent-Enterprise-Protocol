import http from 'node:http';

const port = Number(process.env.MOCK_OPENAI_PORT ?? '8080');
const apiKey = process.env.MOCK_OPENAI_API_KEY ?? 'm1-e2e-provider-secret';
const expectedModel = process.env.MOCK_OPENAI_MODEL ?? 'mock-upstream-chat';

function embeddingVector(text, dims) {
  // Deterministic pseudo-embedding: FNV-1a hash chain over the text. Good
  // enough for E2E wiring: same text -> same vector, different text ->
  // different vector.
  const out = new Array(dims).fill(0);
  let h = 2166136261;
  for (const ch of Buffer.from(text, 'utf8')) {
    h ^= ch;
    h = Math.imul(h, 16777619) >>> 0;
    out[h % dims] += ((h >>> 16) % 21) - 10;
  }
  return out.map(v => v / 64);
}

const server = http.createServer(async (request, response) => {
  if (request.method === 'GET' && request.url === '/healthz') {
    sendJSON(response, 200, {status: 'ok'});
    return;
  }
  if (request.method === 'POST' && request.url === '/v1/embeddings') {
    const body = await readJSON(request);
    if (request.headers.authorization !== `Bearer ${apiKey}`) {
      sendJSON(response, 401, {error: {message: 'bad key'}});
      return;
    }
    const input = Array.isArray(body.input) ? body.input : [body.input];
    const dims = Number(body.dimensions) || 1024;
    sendJSON(response, 200, {
      object: 'list',
      data: input.map((text, index) => ({
        object: 'embedding', index, embedding: embeddingVector(String(text), dims),
      })),
      model: body.model ?? 'mock-embedding',
      usage: {prompt_tokens: 1, total_tokens: 1},
    });
    return;
  }
  // Anthropic passthrough target: the EnvoyFilter route (not ai-proxy)
  // forwards here. Asserts the gateway-side rewrite and credential injection
  // and echoes an anthropic-shaped reply.
  if (request.method === 'POST' && request.url === '/api/anthropic/v1/messages') {
    if (request.headers['x-api-key'] !== apiKey || request.headers.authorization !== `Bearer ${apiKey}`) {
      sendJSON(response, 401, {type: 'error', error: {type: 'authentication_error', message: 'passthrough credential was not injected'}});
      return;
    }
    const body = await readJSON(request);
    if (body.stream === true) {
      response.writeHead(200, {'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache'});
      for (const event of [
        {type: 'message_start', message: {id: 'msg-aep-m1-anthropic', type: 'message', role: 'assistant', model: body.model, content: [], stop_reason: null, usage: {input_tokens: 1, output_tokens: 0}}},
        {type: 'content_block_start', index: 0, content_block: {type: 'text', text: ''}},
        {type: 'content_block_delta', index: 0, delta: {type: 'text_delta', text: 'anthropic passthrough ok'}},
        {type: 'content_block_stop', index: 0},
        {type: 'message_delta', delta: {stop_reason: 'end_turn', stop_sequence: null}, usage: {output_tokens: 2}},
        {type: 'message_stop'},
      ]) response.write(`event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`);
      response.end();
      return;
    }
    sendJSON(response, 200, {
      id: 'msg-aep-m1-anthropic', type: 'message', role: 'assistant', model: body.model,
      content: [{type: 'text', text: `anthropic passthrough ok ${body.model}`}],
      stop_reason: 'end_turn', usage: {input_tokens: 1, output_tokens: 2},
    });
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
  if (request.headers['x-aep-deployment-id'] !== 'demo' || !request.headers['x-aep-user-id'] || !request.headers['x-aep-session-id'] || request.headers['x-aep-model-id'] !== 'enterprise-chat') {
    sendJSON(response, 400, {error: {message: 'trusted AEP identity headers are missing'}});
    return;
  }
  if (request.headers['x-aep-license-id'] || request.headers['x-aep-internal-role']) {
    sendJSON(response, 400, {error: {message: 'untrusted AEP headers reached the provider'}});
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
  if (lastUserText.includes('AEP_KNOWLEDGE_SEARCH')) {
    // Scripted knowledge-search flow: the model calls the knowledge_search
    // tool, then relays the PEP-filtered passages as the final answer.
    const toolResult = body.messages?.find(
      message => message.role === 'tool' && message.tool_call_id === 'call-kn-1',
    );
    if (toolResult) {
      const report = `AEP_KNOWLEDGE_OK ${String(toolResult.content)}`;
      if (body.stream === true) {
        streamCompletion(response, report, 'Knowledge compiled.');
      } else {
        response.setHeader('X-Mock-Provider-Auth', 'accepted');
        sendJSON(response, 200, {
          id: 'chatcmpl-aep-m1', object: 'chat.completion', created: 1, model: expectedModel,
          choices: [{index: 0, message: {role: 'assistant', content: report, reasoning_content: 'Knowledge compiled.'}, finish_reason: 'stop'}],
          usage: {prompt_tokens: 1, completion_tokens: 2, total_tokens: 3},
        });
      }
      return;
    }
    const queryMatch = lastUserText.match(/KNQ:([^\s]+)/);
    const query = queryMatch ? queryMatch[1] : 'department handbook';
    const toolCall = {
      index: 0, id: 'call-kn-1', type: 'function',
      function: {name: 'knowledge_search', arguments: JSON.stringify({query})},
    };
    if (body.stream === true) {
      streamNamedToolCall(response, toolCall, 'Search the knowledge base.');
    } else {
      response.setHeader('X-Mock-Provider-Auth', 'accepted');
      sendJSON(response, 200, {
        id: 'chatcmpl-aep-m1', object: 'chat.completion', created: 1, model: expectedModel,
        choices: [{index: 0, message: {role: 'assistant', content: '', tool_calls: [toolCall]}, finish_reason: 'tool_calls'}],
        usage: {prompt_tokens: 1, completion_tokens: 2, total_tokens: 3},
      });
    }
    return;
  }
  if (lastUserText.includes('A2A_INVOKE')) {
    // Scripted agent-to-agent delegation: the model calls invoke_agent with
    // the peer and message extracted from the prompt, then relays the peer's
    // (act-as scoped) answer as the final reply.
    const toolResult = body.messages?.find(
      message => message.role === 'tool' && message.tool_call_id === 'call-a2a-1',
    );
    if (toolResult) {
      const report = `A2A_OK ${String(toolResult.content)}`;
      if (body.stream === true) {
        streamCompletion(response, report, 'Peer answered.');
      } else {
        response.setHeader('X-Mock-Provider-Auth', 'accepted');
        sendJSON(response, 200, {
          id: 'chatcmpl-aep-m1', object: 'chat.completion', created: 1, model: expectedModel,
          choices: [{index: 0, message: {role: 'assistant', content: report, reasoning_content: 'Peer answered.'}, finish_reason: 'stop'}],
          usage: {prompt_tokens: 1, completion_tokens: 2, total_tokens: 3},
        });
      }
      return;
    }
    const peerMatch = lastUserText.match(/A2A_PEER:([a-zA-Z0-9_-]+)/);
    const msgMatch = lastUserText.match(/A2A_MSG:([\s\S]*?)A2A_END/);
    const args = {peer: peerMatch ? peerMatch[1] : 'agentB', message: (msgMatch ? msgMatch[1] : 'AEP_DEPT_REPORT').trim()};
    const toolCall = {
      index: 0, id: 'call-a2a-1', type: 'function',
      function: {name: 'invoke_agent', arguments: JSON.stringify(args)},
    };
    if (body.stream === true) {
      streamNamedToolCall(response, toolCall, 'Delegate to the peer agent.');
    } else {
      response.setHeader('X-Mock-Provider-Auth', 'accepted');
      sendJSON(response, 200, {
        id: 'chatcmpl-aep-m1', object: 'chat.completion', created: 1, model: expectedModel,
        choices: [{index: 0, message: {role: 'assistant', content: '', tool_calls: [toolCall]}, finish_reason: 'tool_calls'}],
        usage: {prompt_tokens: 1, completion_tokens: 2, total_tokens: 3},
      });
    }
    return;
  }
  if (lastUserText.includes('AEP_DEPT_REPORT')) {
    // Scripted department-report flow for digital-employee E2E: the model
    // first calls the dept_data tool, then relays the (scope-filtered)
    // dataset the runtime returned as the final report text.
    const toolResult = body.messages?.find(
      message => message.role === 'tool' && message.tool_call_id === 'call-dept-data-1',
    );
    if (toolResult) {
      const report = `AEP_DEPT_REPORT_OK ${String(toolResult.content)}`;
      if (body.stream === true) {
        streamCompletion(response, report, 'Department data compiled.');
      } else {
        response.setHeader('X-Mock-Provider-Auth', 'accepted');
        sendJSON(response, 200, {
          id: 'chatcmpl-aep-m1', object: 'chat.completion', created: 1, model: expectedModel,
          choices: [{index: 0, message: {role: 'assistant', content: report, reasoning_content: 'Department data compiled.'}, finish_reason: 'stop'}],
          usage: {prompt_tokens: 1, completion_tokens: 2, total_tokens: 3},
        });
      }
      return;
    }
    const teamMatch = lastUserText.match(/AEP_DEPT_REPORT_TEAM:([a-zA-Z0-9_-]+)/);
    const teamArg = teamMatch ? `,"team":"${teamMatch[1]}"` : '';
    const toolCall = {
      index: 0, id: 'call-dept-data-1', type: 'function',
      function: {name: 'dept_data', arguments: `{"dataset":"monthly_sales"${teamArg}}`},
    };
    if (body.stream === true) {
      streamNamedToolCall(response, toolCall, 'Fetch the department dataset.');
    } else {
      response.setHeader('X-Mock-Provider-Auth', 'accepted');
      sendJSON(response, 200, {
        id: 'chatcmpl-aep-m1', object: 'chat.completion', created: 1, model: expectedModel,
        choices: [{index: 0, message: {role: 'assistant', content: '', tool_calls: [toolCall]}, finish_reason: 'tool_calls'}],
        usage: {prompt_tokens: 1, completion_tokens: 2, total_tokens: 3},
      });
    }
    return;
  }
  response.setHeader('X-Mock-Provider-Auth', 'accepted');
  if (body.stream === true) {
    response.writeHead(200, {'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache'});
    response.write(`data: ${JSON.stringify(chunk('', 'Think through the request.'))}\n\n`);
    response.write(`data: ${JSON.stringify(chunk('Hello'))}\n\n`);
    setTimeout(() => {
      response.write(`data: ${JSON.stringify(chunk(' AEP', undefined, 'stop'))}\n\n`);
      if (body.stream_options?.include_usage) {
        response.write(`data: ${JSON.stringify({id: 'chatcmpl-aep-m1', object: 'chat.completion.chunk', created: 1, model: expectedModel, choices: [], usage: {prompt_tokens: 1, completion_tokens: 2, total_tokens: 3}})}\n\n`);
      }
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

function streamNamedToolCall(response, toolCall, reasoning) {
  response.setHeader('X-Mock-Provider-Auth', 'accepted');
  response.writeHead(200, {'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache'});
  response.write(`data: ${JSON.stringify(chunk('', reasoning))}\n\n`);
  response.write(`data: ${JSON.stringify({
    id: 'chatcmpl-aep-m1', object: 'chat.completion.chunk', created: 1, model: expectedModel,
    choices: [{index: 0, delta: {tool_calls: [toolCall]}, finish_reason: null}],
  })}\n\n`);
  response.write(`data: ${JSON.stringify(chunk('', undefined, 'tool_calls'))}\n\n`);
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

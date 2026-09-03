import {randomUUID} from 'node:crypto';

import {type AepClient, type AgentModel, type ModelConnection} from '@aep/sdk-node';
import OpenAI from 'openai';

import {AgentState} from './state.js';

type ModelControlClient = Pick<AepClient, 'getModelConnection' | 'listModels'>;
type ModelState = Pick<AgentState, 'enqueueTelemetry'>;

export interface ModelChatOptions {
  prompt: string;
  modelId?: string;
  stream?: boolean;
}

export interface ModelChatResult {
  modelId: string;
  responseModel: string;
  content: string;
  streamed: boolean;
}

export interface OpenAIModelClientOptions {
  timeoutMs?: number;
}

export class OpenAIModelClient {
  readonly #timeoutMs: number;

  constructor(
    private readonly client: ModelControlClient,
    private readonly state: ModelState,
    options: OpenAIModelClientOptions = {},
  ) {
    this.#timeoutMs = options.timeoutMs ?? 120_000;
  }

  async chat(options: ModelChatOptions): Promise<ModelChatResult> {
    const startedAt = Date.now();
    let model: AgentModel | undefined;
    try {
      model = await this.#selectModel(options.modelId);
      const connection = await this.client.getModelConnection();
      const result = await this.#complete(connection, model, options);
      this.#queueTelemetry('model.request.completed', 'success', model.id, options.stream === true, startedAt, {
        responseModel: result.responseModel,
        ...result.usage,
      });
      return {
        modelId: model.id,
        responseModel: result.responseModel,
        content: result.content,
        streamed: options.stream === true,
      };
    } catch (error) {
      this.#queueTelemetry('model.request.failed', 'failure', model?.id ?? options.modelId, options.stream === true, startedAt, {
        errorCode: modelErrorCode(error),
        ...modelErrorStatus(error),
      });
      throw error;
    }
  }

  async #selectModel(requestedId?: string): Promise<AgentModel> {
    const {models} = await this.client.listModels();
    const model = requestedId ? models.find(item => item.id === requestedId) : models.find(item => item.isDefault);
    if (!model) {
      const code = requestedId ? 'MODEL_NOT_AVAILABLE' : 'DEFAULT_MODEL_NOT_AVAILABLE';
      throw new ModelClientError(code, requestedId ? `Model ${requestedId} is not available to this Agent.` : 'No default model is available to this Agent.');
    }
    if (!model.enabled) throw new ModelClientError('MODEL_DISABLED', `Model ${model.id} is disabled.`);
    if (model.protocol !== 'openai-compatible') throw new ModelClientError('MODEL_PROTOCOL_UNSUPPORTED', `Model ${model.id} does not use the OpenAI-compatible protocol.`);
    return model;
  }

  async #complete(
    connection: ModelConnection,
    model: AgentModel,
    options: ModelChatOptions,
  ): Promise<{content: string; responseModel: string; usage?: Record<string, number>}> {
    if (connection.protocol !== 'openai-compatible') {
      throw new ModelClientError('GATEWAY_PROTOCOL_UNSUPPORTED', `Gateway protocol ${connection.protocol} is not supported.`);
    }
    const openai = new OpenAI({
      apiKey: connection.apiKey,
      baseURL: connection.baseUrl,
      maxRetries: 0,
      timeout: this.#timeoutMs,
    });
    const messages = [{role: 'user' as const, content: options.prompt}];
    if (options.stream === true) {
      const stream = await openai.chat.completions.create({model: model.id, messages, stream: true});
      let content = '';
      let responseModel = model.id;
      for await (const chunk of stream) {
        responseModel = chunk.model || responseModel;
        content += chunk.choices[0]?.delta.content ?? '';
      }
      return {content, responseModel};
    }
    const completion = await openai.chat.completions.create({model: model.id, messages});
    const usage = completion.usage
      ? {
          promptTokens: completion.usage.prompt_tokens,
          completionTokens: completion.usage.completion_tokens,
          totalTokens: completion.usage.total_tokens,
        }
      : undefined;
    return {
      content: completion.choices[0]?.message.content ?? '',
      responseModel: completion.model,
      ...(usage ? {usage} : {}),
    };
  }

  #queueTelemetry(
    type: 'model.request.completed' | 'model.request.failed',
    result: 'success' | 'failure',
    modelId: string | undefined,
    streamed: boolean,
    startedAt: number,
    data: Record<string, string | number>,
  ): void {
    this.state.enqueueTelemetry({
      eventId: randomUUID(),
      type,
      occurredAt: new Date().toISOString(),
      ...(modelId ? {resource: {type: 'model', id: modelId}} : {}),
      result,
      data: {
        ...(modelId ? {modelId} : {}),
        streamed,
        durationMs: Math.max(0, Date.now() - startedAt),
        ...data,
      },
    });
  }
}

class ModelClientError extends Error {
  constructor(readonly code: string, message: string) {
    super(message);
    this.name = 'ModelClientError';
  }
}

function modelErrorCode(error: unknown): string {
  return error instanceof ModelClientError ? error.code : 'MODEL_REQUEST_FAILED';
}

function modelErrorStatus(error: unknown): {status?: number} {
  if (typeof error !== 'object' || error === null || !('status' in error)) return {};
  const status = (error as {status?: unknown}).status;
  return typeof status === 'number' ? {status} : {};
}

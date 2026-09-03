import {createServer, type IncomingMessage, type Server, type ServerResponse} from 'node:http';
import {once} from 'node:events';

export class MockAepServer {
  #server: Server;
  #validAccessToken = 'access-1';
  #validRefreshToken = 'refresh-1';
  #expireAccess = false;
  #failRefresh = false;
  #metadataFailures = 0;
  #unauthorizedResponseDelays: number[] = [];
  #modelGatewayEnabled = true;
  #credentialNoStore = true;
  #dataPlaneRevision = 'rev-1';
  readonly requests: Array<{method: string; path: string; headers: IncomingMessage['headers']}> = [];
  refreshCount = 0;
  baseUrl = '';

  constructor() {
    this.#server = createServer((request, response) => void this.#handle(request, response));
  }

  async start(): Promise<void> {
    this.#server.listen(0, '127.0.0.1');
    await once(this.#server, 'listening');
    const address = this.#server.address();
    if (!address || typeof address === 'string') throw new Error('Mock server did not bind');
    this.baseUrl = `http://127.0.0.1:${address.port}`;
  }

  async stop(): Promise<void> {
    this.#server.close();
    await once(this.#server, 'close');
  }

  expireAccessToken(): void {
    this.#expireAccess = true;
  }

  failRefresh(): void {
    this.#failRefresh = true;
  }

  failMetadata(times: number): void {
    this.#metadataFailures = times;
  }

  delayUnauthorizedResponses(delays: number[]): void {
    this.#unauthorizedResponseDelays = [...delays];
  }

  disableModelGateway(): void {
    this.#modelGatewayEnabled = false;
  }

  omitCredentialNoStore(): void {
    this.#credentialNoStore = false;
  }

  async #handle(request: IncomingMessage, response: ServerResponse): Promise<void> {
    const path = new URL(request.url ?? '/', this.baseUrl).pathname;
    this.requests.push({method: request.method ?? 'GET', path, headers: request.headers});

    if (path === '/aep/v1/metadata') {
      if (this.#metadataFailures > 0) {
        this.#metadataFailures -= 1;
        return json(response, 503, problem(503, 'TEMPORARILY_UNAVAILABLE'));
      }
      return json(response, 200, {
        service: 'mock-aep',
        supportedProtocolVersions: ['1.0'],
        capabilities: ['password_auth', 'federated_auth', 'skills', 'telemetry', 'control_events', ...(this.#modelGatewayEnabled ? ['model_gateway'] : []), 'credentials'],
        jwksUri: '/.well-known/jwks.json',
        ...(this.#modelGatewayEnabled ? {modelGateway: {baseUrl: '/openai/v1', protocol: 'openai-compatible', apiVersion: 'v1'}} : {}),
      });
    }
    if (path === '/.well-known/jwks.json') {
      return json(response, 200, {keys: [{kty: 'OKP', kid: 'm0', use: 'sig', alg: 'EdDSA', crv: 'Ed25519', x: 'AA'}]});
    }
    if (path === '/aep/v1/auth/methods') {
      return json(response, 200, {
        enterprise: {id: 'ent-1', name: 'Demo'},
        preferredMethodId: 'zhiyuan-password',
        methods: [{id: 'zhiyuan-password', type: 'password', displayName: 'ZhiYuan account'}],
      });
    }
    if (path === '/aep/v1/auth/password/login') {
      this.#expireAccess = false;
      return json(response, 200, tokens(this.#validAccessToken, this.#validRefreshToken));
    }
    if (path === '/aep/v1/auth/refresh') {
      this.refreshCount += 1;
      if (this.#failRefresh) return json(response, 401, problem(401, 'REFRESH_TOKEN_INVALID'));
      this.#validAccessToken = `access-${this.refreshCount + 1}`;
      this.#validRefreshToken = `refresh-${this.refreshCount + 1}`;
      this.#expireAccess = false;
      return json(response, 200, tokens(this.#validAccessToken, this.#validRefreshToken));
    }
    if (path === '/aep/v1/auth/logout') return empty(response, 204);
    if (path === '/aep/v1/auth/password/change') {
      return json(response, 200, tokens(this.#validAccessToken, this.#validRefreshToken));
    }

    if (!this.#authorized(request)) {
      const delay = this.#unauthorizedResponseDelays.shift() ?? 0;
      if (delay > 0) await new Promise(resolve => setTimeout(resolve, delay));
      return json(response, 401, problem(401, 'TOKEN_INVALID'));
    }

    if (path === '/aep/v1/agent/me' || path === '/aep/v1/user/me') {
      return json(response, 200, {
        user: {id: 'user-1', displayName: 'Demo User'},
        enterprise: {id: 'ent-1', name: 'Demo'},
        roles: ['user'],
        sessionExpiresAt: '2026-08-20T01:00:00Z',
        passwordChangeRequired: false,
      });
    }
    if (path === '/aep/v1/agent/activation') {
      return json(response, 200, {
        entitlementToken: 'entitlement-1',
        tokenType: 'Bearer',
        expiresAt: '2026-08-20T01:00:00Z',
        expiresIn: 3600,
        licenseId: 'lic-1',
        licenseDigest: 'sha256:' + 'a'.repeat(64),
        deploymentId: 'deployment-1',
        features: ['enterprise.models'],
      });
    }
    if (path === '/aep/v1/agent/credentials') {
      return json(response, 200, {credentials: [credential('agent')]});
    }
    if (path === '/aep/v1/agent/credentials/credential-1/resolve') {
      if (this.#credentialNoStore) response.setHeader('Cache-Control', 'no-store');
      return json(response, 200, {credentialId: 'credential-1', type: 'api_key', value: 'resolved-secret', expiresAt: null});
    }
    if (path === '/aep/v1/agent/credentials/server-only/resolve') {
      return json(response, 403, problem(403, 'CREDENTIAL_NOT_DELIVERABLE'));
    }
    if (path === '/aep/v1/agent/models' || path === '/aep/v1/user/models') {
      return json(response, 200, {models: [model(false)]});
    }
    if (path === '/aep/v1/agent/skills/manifest') {
      response.setHeader('ETag', '"skills-1"');
      if (request.headers['if-none-match'] === '"skills-1"') return empty(response, 304);
      return json(response, 200, {
        revision: '1',
        generatedAt: '2026-08-20T00:00:00Z',
        skills: [{id: 'review', name: 'Review', version: '1.0.0', enabled: true, package: {url: '/package', sha256: 'abc', size: 4}}],
      });
    }
    if (path.endsWith('/package')) {
      response.statusCode = 200;
      response.setHeader('Content-Type', 'application/zip');
      return void response.end(Buffer.from([0x50, 0x4b, 0x03, 0x04]));
    }
    if (path === '/aep/v1/agent/heartbeat') {
      return json(response, 200, {serverTime: '2026-08-20T00:00:00Z', hasPendingControlEvents: true, controlEventWatermark: '1', nextHeartbeatAfterSeconds: 30});
    }
    if (path === '/aep/v1/agent/control-events') return json(response, 200, {items: [], nextCursor: null});
    if (path.endsWith('/acknowledge') || path.endsWith('/result')) return empty(response, 204);
    if (path === '/aep/v1/agent/skills/sync-results') return empty(response, 202);
    if (path === '/aep/v1/agent/events/batch') return json(response, 200, {accepted: ['event-1'], rejected: []});
    if (path === '/aep/v1/admin/agents') return json(response, 200, {items: [], nextCursor: null});
    if (path === '/aep/v1/admin/agents/missing') return json(response, 404, problem(404, 'RESOURCE_NOT_FOUND'));
    if (path === '/aep/v1/admin/credentials') {
      if (request.method === 'GET') return json(response, 200, {credentials: [credential('agent'), credential('server_only')]});
      return json(response, 201, credential('agent'));
    }
    if (path === '/aep/v1/admin/credentials/credential-1/rotate') return json(response, 200, credential('agent'));
    if (path === '/aep/v1/admin/credentials/credential-1') {
      if (request.method === 'DELETE') return empty(response, 204);
      return json(response, 200, credential('agent'));
    }
    if (path === '/aep/v1/admin/credentials/credential-in-use' && request.method === 'DELETE') {
      return json(response, 409, problem(409, 'CREDENTIAL_IN_USE'));
    }
    if (path === '/aep/v1/admin/credential-assignments') {
      const assignment = credentialAssignment();
      if (request.method === 'GET') return json(response, 200, {assignments: [assignment]});
      return json(response, 201, assignment);
    }
    if (path === '/aep/v1/admin/credential-assignments/credential-assignment-1') return empty(response, 204);
    if (path === '/aep/v1/admin/models') {
      if (request.method === 'GET') return json(response, 200, {models: [model(true)]});
      return json(response, 201, model(true));
    }
    if (path === '/aep/v1/admin/models/model-1') {
      if (request.method === 'DELETE') return empty(response, 204);
      return json(response, 200, model(true));
    }
    if (path === '/aep/v1/admin/model-assignments') {
      const assignment = modelAssignment();
      if (request.method === 'GET') return json(response, 200, {assignments: [assignment]});
      return json(response, 201, assignment);
    }
    if (path === '/aep/v1/admin/model-assignments/assignment-1') return empty(response, 204);
    if (path === '/aep/v1/admin/data-plane/desired-state') {
      if (request.method === 'PUT') {
        this.#dataPlaneRevision = 'rev-published';
        return json(response, 200, dataPlaneDesiredState(this.#dataPlaneRevision));
      }
      return json(response, 200, dataPlaneDesiredState(this.#dataPlaneRevision));
    }
    if (path === '/aep/v1/admin/data-plane/status') {
      return json(response, 200, {state: 'ready', observedRevision: this.#dataPlaneRevision, contentHash: 'a'.repeat(64), lastAppliedAt: '2026-08-24T00:00:00Z', errorCode: null, message: null, resourceCount: 1});
    }
    if (path.startsWith('/aep/v1/admin/')) return json(response, 200, {items: [], nextCursor: null});

    return json(response, 404, problem(404, 'RESOURCE_NOT_FOUND'));
  }

  #authorized(request: IncomingMessage): boolean {
    return !this.#expireAccess && request.headers.authorization === `Bearer ${this.#validAccessToken}`;
  }
}

function tokens(accessToken: string, refreshToken: string): object {
  return {accessToken, refreshToken, modelAccessToken: 'model-1', tokenType: 'Bearer', expiresIn: 300, modelAccessExpiresIn: 300, deploymentId: 'ent-1', sessionId: 'session-1', passwordChangeRequired: false};
}

function credential(deliveryMode: 'agent' | 'server_only'): object {
  return {
    id: deliveryMode === 'agent' ? 'credential-1' : 'server-only',
    name: deliveryMode === 'agent' ? 'Agent API key' : 'Gateway API key',
    service: 'example-service',
    type: 'api_key',
    deliveryMode,
    maskedValue: '****cdef',
    enabled: true,
    updatedAt: '2026-08-21T00:00:00Z',
  };
}

function credentialAssignment(): object {
  return {
    id: 'credential-assignment-1',
    resourceType: 'credential',
    resourceId: 'credential-1',
    subject: {type: 'user', id: 'user-1'},
    createdAt: '2026-08-21T00:00:00Z',
  };
}

function model(admin: boolean): object {
  return {
    id: 'model-1',
    displayName: 'Enterprise Model',
    sourceType: 'gateway',
    protocol: 'openai-compatible',
    endpoint: '/openai/v1',
    upstreamModel: 'qwen3-32b',
    capabilities: ['text', 'tools', 'streaming', 'reasoning'],
    reasoningCompatibility: {
      thinkingFormat: 'deepseek',
      supportsReasoningEffort: true,
      requiresReasoningContentOnAssistantMessages: true,
    },
    contextWindow: 131072,
    isDefault: true,
    enabled: true,
    ...(admin ? {credentialId: null} : {}),
  };
}

function modelAssignment(): object {
  return {
    id: 'assignment-1',
    resourceType: 'model',
    resourceId: 'model-1',
    subject: {type: 'user', id: 'user-1'},
    createdAt: '2026-08-21T00:00:00Z',
  };
}

function dataPlaneDesiredState(revision: string): object {
  return {
    enterpriseId: 'ent-1',
    revision,
    publishedAt: '2026-08-24T00:00:00Z',
    contentHash: 'a'.repeat(64),
    routes: [{modelId: 'model-1', enabled: true, endpoint: '/v1', upstreamModel: 'qwen3-32b', protocol: 'openai-compatible', providerType: 'deepseek', credentialRef: {name: 'provider-secrets', key: 'model-1'}}],
  };
}

function problem(status: number, code: string): object {
  return {type: `https://aep.example/problems/${code.toLowerCase()}`, title: code, status, code, requestId: 'req-mock'};
}

function json(response: ServerResponse, status: number, body: object): void {
  response.statusCode = status;
  response.setHeader('Content-Type', status >= 400 ? 'application/problem+json' : 'application/json');
  response.end(JSON.stringify(body));
}

function empty(response: ServerResponse, status: number): void {
  response.statusCode = status;
  response.end();
}

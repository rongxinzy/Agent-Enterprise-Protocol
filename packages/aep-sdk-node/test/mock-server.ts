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
  readonly requests: Array<{method: string; path: string; search: string; headers: IncomingMessage['headers']}> = [];
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
    const parsedURL = new URL(request.url ?? '/', this.baseUrl);
    this.requests.push({method: request.method ?? 'GET', path, search: parsedURL.search, headers: request.headers});

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
        deployment: {id: 'ent-1', name: 'Demo'},
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
        deployment: {id: 'ent-1', name: 'Demo'},
        deploymentId: 'ent-1',
        roles: ['user'],
        sessionExpiresAt: '2026-08-20T01:00:00Z',
        passwordChangeRequired: false,
      });
    }
    if (path === '/aep/v1/user/activation') {
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
    if (path === '/aep/v1/user/credentials') {
      return json(response, 200, {credentials: [credential('client')]});
    }
    if (path === '/aep/v1/user/credentials/credential-1/resolve') {
      if (this.#credentialNoStore) response.setHeader('Cache-Control', 'no-store');
      return json(response, 200, {credentialId: 'credential-1', type: 'api_key', value: 'resolved-secret', expiresAt: null});
    }
    if (path === '/aep/v1/user/credentials/server-only/resolve') {
      return json(response, 403, problem(403, 'CREDENTIAL_NOT_DELIVERABLE'));
    }
    if (path === '/aep/v1/user/models') {
      return json(response, 200, {models: [model(false)]});
    }
    if (path === '/aep/v1/user/skills/manifest') {
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
    if (path === '/aep/v1/user/heartbeat') {
      return json(response, 200, {serverTime: '2026-08-20T00:00:00Z', controlEvents: {pending: true, watermark: '1'}, nextHeartbeatAfterSeconds: 30});
    }
    if (path === '/aep/v1/user/control-events') return json(response, 200, {items: [], nextCursor: null});
    if (path.endsWith('/acknowledge') || path.endsWith('/result')) return empty(response, 204);
    if (path === '/aep/v1/user/skills/sync-results') return empty(response, 202);
    if (path === '/aep/v1/user/events/batch') return json(response, 200, {accepted: ['event-1'], rejected: []});
    if (path === '/aep/v1/admin/skills/review/versions/1.0.0' && request.method === 'DELETE') return empty(response, 204);
    if (path === '/aep/v1/admin/credentials') {
      if (request.method === 'GET') return json(response, 200, {credentials: [credential('client'), credential('server_only')]});
      return json(response, 201, credential('client'));
    }
    if (path === '/aep/v1/admin/permissions') return json(response, 200, {permissions: [{id: 'models.read', description: 'Read models'}]});
    if (path === '/aep/v1/admin/roles') {
      if (request.method === 'POST') return json(response, 201, role('operator'));
      return json(response, 200, {roles: [role('operator')]});
    }
    if (path === '/aep/v1/admin/roles/operator') {
      if (request.method === 'DELETE') return empty(response, 204);
      return json(response, 200, role('operator'));
    }
    if (path === '/aep/v1/admin/teams') {
      if (request.method === 'POST') return json(response, 201, team('support'));
      return json(response, 200, {teams: [team('support')]});
    }
    if (path === '/aep/v1/admin/teams/support') {
      if (request.method === 'DELETE') return empty(response, 204);
      return json(response, 200, team('support'));
    }
    if (path === '/aep/v1/admin/users/user-1/rbac') return json(response, 200, {userId: 'user-1', roleIds: ['operator'], teamIds: ['support']});
    if (path === '/aep/v1/admin/sessions/session-1/revoke') return empty(response, 204);
    if (path === '/aep/v1/admin/credentials/credential-1/rotate') return json(response, 200, credential('client'));
    if (path === '/aep/v1/admin/credentials/credential-1') {
      if (request.method === 'DELETE') return empty(response, 204);
      return json(response, 200, credential('client'));
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
    if (path === '/aep/v1/admin/models/missing') return json(response, 404, problem(404, 'RESOURCE_NOT_FOUND'));
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
    if (path === '/aep/v1/admin/control-events') return json(response, 200, {items: [], nextCursor: null});
    if (path === '/aep/v1/admin/control-events/event-1') {
      if (request.method === 'POST') return json(response, 200, adminEvent());
      return json(response, 200, adminEvent());
    }
    if (path === '/aep/v1/admin/control-events/event-1/cancel') return json(response, 200, adminEvent());
    if (path === '/aep/v1/admin/licenses') return json(response, 200, {items: [license()]});
    if (path === '/aep/v1/admin/licenses/lic-1') return json(response, 200, license());
    if (path === '/aep/v1/admin/licenses/import') return json(response, 201, license());
    if (path === '/aep/v1/admin/licenses/lic-1/revoke') return json(response, 200, {licenseId: 'lic-1', status: 'revoked'});
    if (path === '/aep/v1/admin/agents') {
      if (request.method === 'POST') {
        const input = await readJson(request);
        if (typeof input.username !== 'string' || !/^[A-Za-z0-9._-]+$/.test(input.username)) {
          return json(response, 400, problem(400, 'INVALID_AGENT'));
        }
        return json(response, 201, agentResponse());
      }
      return json(response, 200, {agents: [agentDirectoryEntry()], nextCursor: null});
    }
    if (path === '/aep/v1/admin/agents/agent-1/profile' && request.method === 'PUT') {
      const input = await readJson(request);
      return json(response, 200, {
        id: 'agent-1',
        homeTeamId: typeof input.homeTeamId === 'string' ? input.homeTeamId : 'team-1',
        displayTitle: typeof input.displayTitle === 'string' ? input.displayTitle : 'Reviewer',
      });
    }
    if (path === '/aep/v1/admin/identity-sources') {
      if (request.method === 'POST') {
        const input = await readJson(request);
        return json(response, 201, {
          id: typeof input.id === 'string' ? input.id : 'ldap-1',
          kind: input.kind ?? 'ldap',
          displayName: typeof input.displayName === 'string' ? input.displayName : 'Corporate LDAP',
          enabled: true,
        });
      }
      return json(response, 200, {identitySources: [identitySource()], nextCursor: null});
    }
    if (path === '/aep/v1/admin/identity-sources/ldap-1/mappings') {
      if (request.method === 'PUT') {
        const input = await readJson(request);
        return json(response, 201, {
          sourceId: 'ldap-1',
          externalId: typeof input.externalId === 'string' ? input.externalId : 'ext-user-1',
          localSubjectId: typeof input.localSubjectId === 'string' ? input.localSubjectId : 'user-1',
        });
      }
      return json(response, 200, {mappings: [identityMapping()], nextCursor: null});
    }
    if (path === '/aep/v1/admin/identity-sources/ldap-1/mappings/user/ext-user-1' && request.method === 'DELETE') {
      return empty(response, 204);
    }
    if (path === '/aep/v1/admin/data-scope-rules') {
      if (request.method === 'POST') return json(response, 201, dataScopeRule());
      return json(response, 200, {rules: [dataScopeRule()], nextCursor: null});
    }
    if (path === '/aep/v1/admin/data-scope-rules/rule-1') {
      if (request.method === 'DELETE') return empty(response, 204);
      return json(response, 200, dataScopeRule());
    }
    if (path === '/aep/v1/admin/data-scope/context') {
      const userId = parsedURL.searchParams.get('userId');
      if (!userId) return json(response, 400, problem(400, 'USER_REQUIRED'));
      return json(response, 200, retrievalContext());
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

function credential(deliveryMode: 'client' | 'server_only'): object {
  return {
    id: deliveryMode === 'client' ? 'credential-1' : 'Gateway API key',
    name: deliveryMode === 'client' ? 'Client API key' : 'Gateway API key',
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

function role(id: string): object {
  return {id, name: 'Operator', description: '', builtIn: false, enabled: true, permissions: ['models.read']};
}

function team(id: string): object {
  return {id, name: 'Support', description: '', builtIn: false, enabled: true, memberCount: 1};
}

function adminEvent(): object {
  return {eventId: 'event-1', type: 'model.catalog.changed', scope: {type: 'global'}, task: {type: 'model.reconcile'}, createdAt: '2026-08-24T00:00:00Z', expiresAt: '2026-08-25T00:00:00Z', state: 'active'};
}

function license(): object {
  return {licenseId: 'lic-1', customerId: 'customer-1', deploymentId: 'ent-1', edition: 'enterprise', status: 'active', issuedAt: '2026-08-01T00:00:00Z', expiresAt: '2027-08-01T00:00:00Z', features: ['enterprise.models']};
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
    deploymentId: 'ent-1',
    revision,
    publishedAt: '2026-08-24T00:00:00Z',
    contentHash: 'a'.repeat(64),
    routes: [{modelId: 'model-1', enabled: true, endpoint: '/v1', upstreamModel: 'qwen3-32b', protocol: 'openai-compatible', providerType: 'deepseek', credentialRef: {name: 'provider-secrets', key: 'model-1'}}],
  };
}

function problem(status: number, code: string): object {
  return {type: `https://aep.example/problems/${code.toLowerCase()}`, title: code, status, code, requestId: 'req-mock'};
}

function agentDirectoryEntry(): object {
  return {
    id: 'agent-1',
    username: 'review-agent',
    displayName: 'Review Agent',
    status: 'active',
    online: true,
    lastHeartbeatAt: '2026-09-01T00:00:00Z',
    homeTeamId: 'team-1',
    displayTitle: 'Reviewer',
    description: null,
    promptSkillId: null,
  };
}

function agentResponse(): object {
  return {id: 'agent-1', username: 'review-agent', displayName: 'Review Agent', homeTeamId: 'team-1', displayTitle: 'Reviewer'};
}

function identitySource(): object {
  return {id: 'ldap-1', kind: 'ldap', displayName: 'Corporate LDAP', enabled: true};
}

function identityMapping(): object {
  return {sourceId: 'ldap-1', externalSubjectType: 'user', externalId: 'ext-user-1', localSubjectId: 'user-1', status: 'active'};
}

function dataScopeRule(): object {
  return {
    id: 'rule-1',
    ruleKind: 'exception_grant',
    subjectType: 'user',
    subjectId: 'user-1',
    resourceKind: 'team',
    resourceId: 'team-9',
    startsAt: null,
    expiresAt: '2027-01-01T00:00:00Z',
    reason: 'Cross-department review',
  };
}

function retrievalContext(): object {
  return {
    principalId: 'user-1',
    deploymentId: 'ent-1',
    orgScope: ['team-1'],
    ownTeamIds: ['team-1'],
    roleScope: ['operator'],
    allowedResources: [{kind: 'team', id: 'team-9'}],
    deniedResources: [],
    crossDepartmentReason: 'Cross-department review',
  };
}

async function readJson(request: IncomingMessage): Promise<Record<string, unknown>> {
  const chunks: Buffer[] = [];
  for await (const chunk of request) chunks.push(chunk as Buffer);
  const raw = Buffer.concat(chunks).toString('utf8').trim();
  return raw ? (JSON.parse(raw) as Record<string, unknown>) : {};
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

import {AEP_PROTOCOL_VERSION, HttpMethod} from './constants.js';
import {AepProblem} from './problem.js';
import {FetchTransport} from './transport.js';
import type {
  AdminAgent,
  AepClientOptions,
  AepRequest,
  AepResponse,
  AepTokens,
  AepTransport,
  AuthenticationMethods,
  ControlEventPage,
  CurrentIdentity,
  HeartbeatResponse,
  JsonObject,
  JsonValue,
  Page,
  PlatformUser,
  Query,
  ServiceMetadata,
  SkillManifest,
  SkillManifestResult,
} from './types.js';

export class AepClient {
  readonly #baseUrl: string;
  readonly #agentId: string;
  readonly #agentVersion: string;
  readonly #platform: AepClientOptions['platform'];
  readonly #tokenStore: AepClientOptions['tokenStore'];
  readonly #transport: AepTransport;
  #refreshPromise: Promise<AepTokens> | null = null;

  constructor(options: AepClientOptions) {
    this.#baseUrl = options.baseUrl;
    this.#agentId = options.agentId;
    this.#agentVersion = options.agentVersion;
    this.#platform = options.platform;
    this.#tokenStore = options.tokenStore;
    this.#transport = options.transport ?? new FetchTransport();
  }

  getMetadata(): Promise<ServiceMetadata> {
    return this.#send({method: HttpMethod.Get, path: '/aep/v1/metadata'}, false);
  }

  getJwks(): Promise<JsonObject> {
    return this.#send({method: HttpMethod.Get, path: '/.well-known/jwks.json'}, false);
  }

  getAuthenticationMethods(enterpriseHint: string): Promise<AuthenticationMethods> {
    return this.#send(
      {method: HttpMethod.Get, path: `/aep/v1/auth/methods?${query({enterpriseHint})}`},
      false,
    );
  }

  async loginWithPassword(input: {
    enterpriseId: string;
    username: string;
    password: string;
  }): Promise<AepTokens> {
    const tokens = await this.#send<AepTokens>(
      {
        method: HttpMethod.Post,
        path: '/aep/v1/auth/password/login',
        body: asJson({...this.#agentContext(), ...input}),
      },
      false,
    );
    await this.#tokenStore.set(tokens);
    return tokens;
  }

  startFederatedLogin(input: {
    enterpriseId: string;
    methodId: string;
    redirectUri: string;
    codeChallenge: string;
    codeChallengeMethod?: 'S256';
  }): Promise<JsonObject> {
    return this.#send(
      {method: HttpMethod.Post, path: '/aep/v1/auth/federated/start', body: asJson(input)},
      false,
    );
  }

  async exchangeFederatedAuthorizationCode(input: {
    transactionId: string;
    authorizationCode: string;
    redirectUri: string;
    codeVerifier: string;
  }): Promise<AepTokens> {
    const tokens = await this.#send<AepTokens>(
      {
        method: HttpMethod.Post,
        path: '/aep/v1/auth/exchange',
        body: asJson({...this.#agentContext(), ...input}),
      },
      false,
    );
    await this.#tokenStore.set(tokens);
    return tokens;
  }

  async changePassword(currentPassword: string, newPassword: string): Promise<AepTokens> {
    const tokens = await this.#send<AepTokens>({
      method: HttpMethod.Post,
      path: '/aep/v1/auth/password/change',
      body: {currentPassword, newPassword, agentId: this.#agentId},
    });
    await this.#tokenStore.set(tokens);
    return tokens;
  }

  async refreshSession(): Promise<AepTokens> {
    return this.#refresh();
  }

  async logout(): Promise<void> {
    const tokens = await this.#tokenStore.get();
    if (!tokens) return;
    try {
      await this.#send<void>({
        method: HttpMethod.Post,
        path: '/aep/v1/auth/logout',
        body: {refreshToken: tokens.refreshToken},
        responseType: 'empty',
      });
    } finally {
      await this.#tokenStore.clear();
    }
  }

  getCurrentIdentity(): Promise<CurrentIdentity> {
    return this.#send({method: HttpMethod.Get, path: '/aep/v1/agent/me'});
  }

  async getSkillManifest(etag?: string): Promise<SkillManifestResult> {
    const response = await this.#request<SkillManifest>({
      method: HttpMethod.Get,
      path: '/aep/v1/agent/skills/manifest',
      headers: etag ? {'If-None-Match': etag} : undefined,
    });
    const responseEtag = response.headers.get('ETag');
    if (response.status === 304) return {notModified: true, etag: responseEtag ?? etag ?? null};
    this.#assertSuccess(response);
    return {notModified: false, etag: responseEtag, manifest: response.data};
  }

  downloadSkillPackage(skillId: string, version: string): Promise<Uint8Array> {
    return this.#send({
      method: HttpMethod.Get,
      path: `/aep/v1/agent/skills/${segment(skillId)}/versions/${segment(version)}/package`,
      responseType: 'bytes',
    });
  }

  reportSkillSyncResult(result: JsonObject): Promise<void> {
    return this.#send({
      method: HttpMethod.Post,
      path: '/aep/v1/agent/skills/sync-results',
      body: result,
      responseType: 'empty',
    });
  }

  uploadEventBatch(events: JsonObject[]): Promise<JsonObject> {
    return this.#send({
      method: HttpMethod.Post,
      path: '/aep/v1/agent/events/batch',
      body: {events},
    });
  }

  heartbeat(input: JsonObject): Promise<HeartbeatResponse> {
    return this.#send({method: HttpMethod.Post, path: '/aep/v1/agent/heartbeat', body: input});
  }

  listControlEvents(afterCursor?: string, limit = 50): Promise<ControlEventPage> {
    return this.#send({
      method: HttpMethod.Get,
      path: `/aep/v1/agent/control-events?${query({afterCursor, limit})}`,
    });
  }

  acknowledgeControlEvent(deliveryId: string, receivedAt: string): Promise<void> {
    return this.#send({
      method: HttpMethod.Post,
      path: `/aep/v1/agent/control-events/${segment(deliveryId)}/acknowledge`,
      body: {status: 'received', receivedAt},
      responseType: 'empty',
    });
  }

  reportControlEventResult(deliveryId: string, result: JsonObject): Promise<void> {
    return this.#send({
      method: HttpMethod.Post,
      path: `/aep/v1/agent/control-events/${segment(deliveryId)}/result`,
      body: result,
      responseType: 'empty',
    });
  }

  listUsers(cursor?: string, limit = 50): Promise<Page<PlatformUser>> {
    return this.#send({
      method: HttpMethod.Get,
      path: `/aep/v1/admin/users?${query({cursor, limit})}`,
    });
  }

  createUser(input: JsonObject): Promise<PlatformUser> {
    return this.#send({method: HttpMethod.Post, path: '/aep/v1/admin/users', body: input});
  }

  importUsers(input: JsonObject): Promise<JsonObject> {
    return this.#send({method: HttpMethod.Post, path: '/aep/v1/admin/users/import', body: input});
  }

  updateUser(userId: string, input: JsonObject): Promise<PlatformUser> {
    return this.#send({
      method: HttpMethod.Patch,
      path: `/aep/v1/admin/users/${segment(userId)}`,
      body: input,
    });
  }

  resetUserPassword(userId: string, input: JsonObject): Promise<void> {
    return this.#send({
      method: HttpMethod.Post,
      path: `/aep/v1/admin/users/${segment(userId)}/reset-password`,
      body: input,
      responseType: 'empty',
    });
  }

  listSkills(): Promise<JsonObject> {
    return this.#send({method: HttpMethod.Get, path: '/aep/v1/admin/skills'});
  }

  createSkill(input: JsonObject): Promise<JsonObject> {
    return this.#send({method: HttpMethod.Post, path: '/aep/v1/admin/skills', body: input});
  }

  updateSkill(skillId: string, input: JsonObject): Promise<JsonObject> {
    return this.#send({
      method: HttpMethod.Patch,
      path: `/aep/v1/admin/skills/${segment(skillId)}`,
      body: input,
    });
  }

  deleteSkill(skillId: string): Promise<void> {
    return this.#send({
      method: HttpMethod.Delete,
      path: `/aep/v1/admin/skills/${segment(skillId)}`,
      responseType: 'empty',
    });
  }

  uploadSkillVersion(skillId: string, version: string, archive: Uint8Array): Promise<JsonObject> {
    const form = new FormData();
    form.set('version', version);
    const archiveBuffer = archive.buffer.slice(
      archive.byteOffset,
      archive.byteOffset + archive.byteLength,
    ) as ArrayBuffer;
    form.set('package', new Blob([archiveBuffer], {type: 'application/zip'}), skillId + '-' + version + '.zip');
    return this.#send({
      method: HttpMethod.Post,
      path: `/aep/v1/admin/skills/${segment(skillId)}/versions`,
      body: form,
    });
  }

  publishSkillVersion(skillId: string, version: string): Promise<JsonObject> {
    return this.#send({
      method: HttpMethod.Post,
      path: `/aep/v1/admin/skills/${segment(skillId)}/versions/${segment(version)}/publish`,
    });
  }

  listSkillAssignments(): Promise<JsonObject> {
    return this.#send({method: HttpMethod.Get, path: '/aep/v1/admin/skill-assignments'});
  }

  createSkillAssignment(input: JsonObject): Promise<JsonObject> {
    return this.#send({
      method: HttpMethod.Post,
      path: '/aep/v1/admin/skill-assignments',
      body: input,
    });
  }

  deleteSkillAssignment(assignmentId: string): Promise<void> {
    return this.#send({
      method: HttpMethod.Delete,
      path: `/aep/v1/admin/skill-assignments/${segment(assignmentId)}`,
      responseType: 'empty',
    });
  }

  createControlEvent(input: JsonObject): Promise<JsonObject> {
    return this.#send({method: HttpMethod.Post, path: '/aep/v1/admin/control-events', body: input});
  }

  listControlEventDeliveries(eventId: string, filters: Query = {}): Promise<JsonObject> {
    return this.#send({
      method: HttpMethod.Get,
      path: `/aep/v1/admin/control-events/${segment(eventId)}/deliveries?${query(filters)}`,
    });
  }

  listAgents(filters: Query = {}): Promise<Page<AdminAgent>> {
    return this.#send({method: HttpMethod.Get, path: `/aep/v1/admin/agents?${query(filters)}`});
  }

  getAgent(agentId: string): Promise<AdminAgent> {
    return this.#send({method: HttpMethod.Get, path: `/aep/v1/admin/agents/${segment(agentId)}`});
  }

  searchEvents(filters: Query = {}): Promise<JsonObject> {
    return this.#send({method: HttpMethod.Get, path: `/aep/v1/admin/events?${query(filters)}`});
  }

  async #send<T>(request: AepRequest, authenticated = true): Promise<T> {
    const response = await this.#request<T>(request, authenticated);
    this.#assertSuccess(response);
    return response.data;
  }

  async #request<T>(request: AepRequest, authenticated = true): Promise<AepResponse<T>> {
    const headers = {...request.headers, ...this.#agentHeaders()};
    if (authenticated) {
      const tokens = await this.#tokenStore.get();
      if (tokens) headers.Authorization = `Bearer ${tokens.accessToken}`;
    }

    let response = await this.#transport.request<T>(this.#baseUrl, {...request, headers});
    if (authenticated && response.status === 401 && request.path !== '/aep/v1/auth/refresh') {
      const tokens = await this.#tokenStore.get();
      if (tokens) {
        const refreshed = await this.#refresh();
        headers.Authorization = `Bearer ${refreshed.accessToken}`;
        response = await this.#transport.request<T>(this.#baseUrl, {...request, headers});
      }
    }
    return response;
  }

  async #refresh(): Promise<AepTokens> {
    if (this.#refreshPromise) return this.#refreshPromise;
    this.#refreshPromise = (async () => {
      const current = await this.#tokenStore.get();
      if (!current) throw new AepProblem({type: 'about:blank', title: 'No session', status: 401, code: 'NO_SESSION'});
      const response = await this.#transport.request<AepTokens>(this.#baseUrl, {
        method: HttpMethod.Post,
        path: '/aep/v1/auth/refresh',
        headers: this.#agentHeaders(),
        body: {refreshToken: current.refreshToken, agentId: this.#agentId},
      });
      if (response.status < 200 || response.status >= 300) {
        await this.#tokenStore.clear();
        throw AepProblem.from(response.status, response.data, response.headers.get('X-Request-ID'));
      }
      await this.#tokenStore.set(response.data);
      return response.data;
    })();
    try {
      return await this.#refreshPromise;
    } finally {
      this.#refreshPromise = null;
    }
  }

  #assertSuccess<T>(response: AepResponse<T>): void {
    if (response.status >= 200 && response.status < 300) return;
    throw AepProblem.from(response.status, response.data, response.headers.get('X-Request-ID'));
  }

  #agentHeaders(): Record<string, string> {
    return {
      'X-AEP-Agent-ID': this.#agentId,
      'X-AEP-Protocol-Version': AEP_PROTOCOL_VERSION,
      'X-Request-ID': crypto.randomUUID(),
    };
  }

  #agentContext(): object {
    return {agentId: this.#agentId, agentVersion: this.#agentVersion, platform: this.#platform};
  }
}

function segment(value: string): string {
  return encodeURIComponent(value);
}

function query(values: Query): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(values)) {
    if (value !== undefined && value !== null) search.set(key, String(value));
  }
  return search.toString();
}

function asJson(value: unknown): JsonValue {
  return value as JsonValue;
}

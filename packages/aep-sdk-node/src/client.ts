import {AEP_PROTOCOL_VERSION, AepCapability, HttpMethod} from './constants.js';
import {AepProblem} from './problem.js';
import {FetchTransport} from './transport.js';
import type {
  AdminAgent,
  AdminModel,
  AdminModelList,
  AdminModelPatch,
  AdminModelWrite,
  AepClientOptions,
  AepRequest,
  AepResponse,
  AepSessionState,
  AepTokens,
  AepTransport,
  AgentModelList,
  AuthenticationMethods,
  ControlEventPage,
  CredentialAssignment,
  CredentialAssignmentList,
  CredentialAssignmentWrite,
  CredentialCreate,
  CredentialList,
  CredentialMetadata,
  CredentialPatch,
  CredentialRotate,
  CurrentIdentity,
  DataPlaneDesiredState,
  DataPlaneDesiredStateWrite,
  DataPlaneStatus,
  EntitlementTokenResponse,
  HeartbeatResponse,
  JsonObject,
  JsonValue,
  LicenseActivationRequest,
  ModelAssignment,
  ModelAssignmentList,
  ModelAssignmentWrite,
  ModelConnection,
  Page,
  PlatformUser,
  Query,
  ResolvedCredential,
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

  async getSessionState(): Promise<AepSessionState> {
    const tokens = await this.#tokenStore.get();
    if (tokens) {
      return {status: 'authenticated', passwordChangeRequired: tokens.passwordChangeRequired};
    }
    const refreshToken = await this.#tokenStore.getRefreshToken?.();
    return refreshToken ? {status: 'recoverable'} : {status: 'signed-out'};
  }

  async restoreSession(): Promise<AepTokens | null> {
    const tokens = await this.#tokenStore.get();
    if (tokens) return tokens;
    const refreshToken = await this.#tokenStore.getRefreshToken?.();
    if (!refreshToken) return null;
    return this.#refresh(refreshToken);
  }

  async logout(): Promise<void> {
    let tokens = await this.#tokenStore.get();
    try {
      tokens ??= await this.restoreSession();
      if (!tokens) return;
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

  activateEnterpriseLicense(input: LicenseActivationRequest): Promise<EntitlementTokenResponse> {
    return this.#send({
      method: HttpMethod.Post,
      path: '/aep/v1/agent/activation',
      body: asJson(input),
    });
  }

  listAgentCredentials(): Promise<CredentialList> {
    return this.#send({method: HttpMethod.Get, path: '/aep/v1/agent/credentials'});
  }

  async resolveAgentCredential(credentialId: string, purpose: string): Promise<ResolvedCredential> {
    const response = await this.#request<ResolvedCredential>({
      method: HttpMethod.Post,
      path: '/aep/v1/agent/credentials/' + segment(credentialId) + '/resolve',
      body: {purpose},
      retry: false,
    });
    this.#assertSuccess(response);
    if (!hasNoStore(response.headers)) {
      throw new AepProblem({
        type: 'https://aep.example/problems/credential-response-cacheable',
        title: 'Credential response is missing Cache-Control: no-store',
        status: 502,
        code: 'CREDENTIAL_RESPONSE_CACHEABLE',
      });
    }
    return response.data;
  }

  listAgentModels(): Promise<AgentModelList> {
    return this.#send({method: HttpMethod.Get, path: '/aep/v1/agent/models'});
  }

  async getModelConnection(): Promise<ModelConnection> {
    const [metadata, tokens] = await Promise.all([this.getMetadata(), this.#loadOrRestoreTokens()]);
    if (!metadata.capabilities.includes(AepCapability.ModelGateway) || !metadata.modelGateway) {
      throw new AepProblem({
        type: 'https://aep.example/problems/capability-not-supported',
        title: 'Model gateway is not supported',
        status: 501,
        code: 'CAPABILITY_NOT_SUPPORTED',
      });
    }
    if (!tokens) {
      throw new AepProblem({
        type: 'https://aep.example/problems/no-session',
        title: 'No active AEP session',
        status: 401,
        code: 'NO_SESSION',
      });
    }
    return {
      ...metadata.modelGateway,
      baseUrl: new URL(metadata.modelGateway.baseUrl, this.#baseUrl).toString(),
      apiKey: tokens.modelAccessToken,
      expiresIn: tokens.modelAccessExpiresIn,
    };
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

  listCredentials(): Promise<CredentialList> {
    return this.#send({method: HttpMethod.Get, path: '/aep/v1/admin/credentials'});
  }

  createCredential(input: CredentialCreate): Promise<CredentialMetadata> {
    return this.#send({method: HttpMethod.Post, path: '/aep/v1/admin/credentials', body: asJson(input)});
  }

  getCredential(credentialId: string): Promise<CredentialMetadata> {
    return this.#send({method: HttpMethod.Get, path: '/aep/v1/admin/credentials/' + segment(credentialId)});
  }

  updateCredential(credentialId: string, input: CredentialPatch): Promise<CredentialMetadata> {
    return this.#send({
      method: HttpMethod.Patch,
      path: '/aep/v1/admin/credentials/' + segment(credentialId),
      body: asJson(input),
    });
  }

  rotateCredential(credentialId: string, input: CredentialRotate): Promise<CredentialMetadata> {
    return this.#send({
      method: HttpMethod.Post,
      path: '/aep/v1/admin/credentials/' + segment(credentialId) + '/rotate',
      body: asJson(input),
      retry: false,
    });
  }

  deleteCredential(credentialId: string): Promise<void> {
    return this.#send({
      method: HttpMethod.Delete,
      path: '/aep/v1/admin/credentials/' + segment(credentialId),
      responseType: 'empty',
    });
  }

  listCredentialAssignments(): Promise<CredentialAssignmentList> {
    return this.#send({method: HttpMethod.Get, path: '/aep/v1/admin/credential-assignments'});
  }

  createCredentialAssignment(input: CredentialAssignmentWrite): Promise<CredentialAssignment> {
    return this.#send({
      method: HttpMethod.Post,
      path: '/aep/v1/admin/credential-assignments',
      body: asJson(input),
    });
  }

  deleteCredentialAssignment(assignmentId: string): Promise<void> {
    return this.#send({
      method: HttpMethod.Delete,
      path: '/aep/v1/admin/credential-assignments/' + segment(assignmentId),
      responseType: 'empty',
    });
  }

  listAdminModels(): Promise<AdminModelList> {
    return this.#send({method: HttpMethod.Get, path: '/aep/v1/admin/models'});
  }

  createModel(input: AdminModelWrite): Promise<AdminModel> {
    return this.#send({method: HttpMethod.Post, path: '/aep/v1/admin/models', body: asJson(input)});
  }

  getModel(modelId: string): Promise<AdminModel> {
    return this.#send({method: HttpMethod.Get, path: `/aep/v1/admin/models/${segment(modelId)}`});
  }

  updateModel(modelId: string, input: AdminModelPatch): Promise<AdminModel> {
    return this.#send({
      method: HttpMethod.Patch,
      path: `/aep/v1/admin/models/${segment(modelId)}`,
      body: asJson(input),
    });
  }

  deleteModel(modelId: string): Promise<void> {
    return this.#send({
      method: HttpMethod.Delete,
      path: `/aep/v1/admin/models/${segment(modelId)}`,
      responseType: 'empty',
    });
  }

  listModelAssignments(): Promise<ModelAssignmentList> {
    return this.#send({method: HttpMethod.Get, path: '/aep/v1/admin/model-assignments'});
  }

  createModelAssignment(input: ModelAssignmentWrite): Promise<ModelAssignment> {
    return this.#send({
      method: HttpMethod.Post,
      path: '/aep/v1/admin/model-assignments',
      body: asJson(input),
    });
  }

  deleteModelAssignment(assignmentId: string): Promise<void> {
    return this.#send({
      method: HttpMethod.Delete,
      path: `/aep/v1/admin/model-assignments/${segment(assignmentId)}`,
      responseType: 'empty',
    });
  }

  getDataPlaneDesiredState(): Promise<DataPlaneDesiredState> {
    return this.#send({method: HttpMethod.Get, path: '/aep/v1/admin/data-plane/desired-state'});
  }

  putDataPlaneDesiredState(input: DataPlaneDesiredStateWrite): Promise<DataPlaneDesiredState> {
    return this.#send({
      method: HttpMethod.Put,
      path: '/aep/v1/admin/data-plane/desired-state',
      body: asJson(input),
    });
  }

  getDataPlaneStatus(): Promise<DataPlaneStatus> {
    return this.#send({method: HttpMethod.Get, path: '/aep/v1/admin/data-plane/status'});
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
    let usedAccessToken: string | null = null;
    if (authenticated) {
      const tokens = await this.#loadOrRestoreTokens();
      if (tokens) {
        usedAccessToken = tokens.accessToken;
        headers.Authorization = `Bearer ${tokens.accessToken}`;
      }
    }

    let response = await this.#transport.request<T>(this.#baseUrl, {...request, headers});
    if (authenticated && response.status === 401 && request.path !== '/aep/v1/auth/refresh') {
      const tokens = await this.#tokenStore.get();
      if (tokens) {
        const retryTokens = tokens.accessToken === usedAccessToken ? await this.#refresh() : tokens;
        headers.Authorization = `Bearer ${retryTokens.accessToken}`;
        response = await this.#transport.request<T>(this.#baseUrl, {...request, headers});
      }
    }
    return response;
  }

  async #loadOrRestoreTokens(): Promise<AepTokens | null> {
    const tokens = await this.#tokenStore.get();
    if (tokens) return tokens;
    const refreshToken = await this.#tokenStore.getRefreshToken?.();
    return refreshToken ? this.#refresh(refreshToken) : null;
  }

  async #refresh(storedRefreshToken?: string): Promise<AepTokens> {
    if (this.#refreshPromise) return this.#refreshPromise;
    this.#refreshPromise = (async () => {
      const current = await this.#tokenStore.get();
      const refreshToken = current?.refreshToken ?? storedRefreshToken ?? await this.#tokenStore.getRefreshToken?.();
      if (!refreshToken) throw new AepProblem({type: 'about:blank', title: 'No session', status: 401, code: 'NO_SESSION'});
      const response = await this.#transport.request<AepTokens>(this.#baseUrl, {
        method: HttpMethod.Post,
        path: '/aep/v1/auth/refresh',
        headers: this.#agentHeaders(),
        body: {refreshToken, agentId: this.#agentId},
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
      'X-Request-ID': requestId(),
    };
  }

  #agentContext(): object {
    return {agentId: this.#agentId, agentVersion: this.#agentVersion, platform: this.#platform};
  }
}

// Some embedded Electron/webview contexts do not expose Web Crypto. Request
// correlation must still work there, so keep a non-cryptographic fallback.
function requestId(): string {
  const cryptoApi = (globalThis as typeof globalThis & {
    readonly crypto?: {
      randomUUID?: () => string;
      getRandomValues?: (array: Uint8Array) => Uint8Array;
    };
  }).crypto;
  const randomUUID = cryptoApi?.randomUUID;
  if (typeof randomUUID === 'function') return randomUUID.call(cryptoApi);

  const bytes = new Uint8Array(16);
  const getRandomValues = cryptoApi?.getRandomValues;
  if (typeof getRandomValues === 'function') {
    getRandomValues.call(cryptoApi, bytes);
  } else {
    for (let index = 0; index < bytes.length; index += 1) bytes[index] = Math.floor(Math.random() * 256);
  }
  bytes[6] = (bytes[6]! & 0x0f) | 0x40;
  bytes[8] = (bytes[8]! & 0x3f) | 0x80;
  const hex = Array.from(bytes, value => value.toString(16).padStart(2, '0'));
  return `${hex.slice(0, 4).join('')}-${hex.slice(4, 6).join('')}-${hex.slice(6, 8).join('')}-${hex.slice(8, 10).join('')}-${hex.slice(10, 16).join('')}`;
}

function hasNoStore(headers: Headers): boolean {
  return (headers.get('Cache-Control') ?? '')
    .split(',')
    .some(value => value.trim().toLowerCase() === 'no-store');
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

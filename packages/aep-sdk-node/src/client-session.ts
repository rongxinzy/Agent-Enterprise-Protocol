import {AEP_PROTOCOL_VERSION, AepCapability, HttpMethod} from './constants.js';
import {AepProblem} from './problem.js';
import {FetchTransport} from './transport.js';
import {asJson, query, segment} from './client-values.js';
import type {
  JwtVerifierConfiguration,
  AgentModelList,
  AepClientOptions,
  AepRequest,
  AepResponse,
  AepSessionState,
  AepTokens,
  AepTransport,
  UserModelList,
  AuthenticationMethods,
  ClientIdentity,
  ControlEventPage,
  CredentialList,
  CurrentIdentity,
  EntitlementTokenResponse,
  HeartbeatResponse,
  JsonObject,
  LicenseActivationRequest,
  ModelConnection,
  Query,
  ResolvedCredential,
  ServiceMetadata,
  SkillManifest,
  SkillManifestResult,
} from './types.js';

export class AepSessionClient {
  readonly #baseUrl: string;
  readonly #agentControlBaseUrl: string | undefined;
  #sessionId?: string;
  readonly #tokenStore: AepClientOptions['tokenStore'];
  readonly #transport: AepTransport;
  #refreshPromise: Promise<AepTokens> | null = null;

  constructor(options: AepClientOptions) {
    this.#baseUrl = options.baseUrl;
    this.#agentControlBaseUrl = options.agentControlBaseUrl;
    this.#sessionId = options.sessionId;
    this.#tokenStore = options.tokenStore;
    this.#transport = options.transport ?? new FetchTransport();
  }

  /**
   * Split deployments serve the agent control protocol (auth sessions and
   * the /aep/v1/user runtime surface) from its own process; discovery and
   * management endpoints stay on the API base. Unset agentControlBaseUrl
   * keeps everything on one host.
   */
  #baseFor(path: string): string {
    const isAgentSurface = path.startsWith('/aep/v1/user/') || path.startsWith('/aep/v1/auth/');
    return isAgentSurface && this.#agentControlBaseUrl ? this.#agentControlBaseUrl : this.#baseUrl;
  }

  getMetadata(): Promise<ServiceMetadata> {
    return this.send({method: HttpMethod.Get, path: '/aep/v1/metadata'}, false);
  }

  getJwks(): Promise<JsonObject> {
    return this.send({method: HttpMethod.Get, path: '/.well-known/jwks.json'}, false);
  }

  getJwtVerifierConfiguration(): Promise<JwtVerifierConfiguration> {
    return this.send({method: HttpMethod.Get, path: '/.well-known/openid-configuration'}, false);
  }

  getAuthenticationMethods(deploymentHint: string): Promise<AuthenticationMethods> {
    return this.send(
      {method: HttpMethod.Get, path: `/aep/v1/auth/methods?${query({deploymentHint})}`},
      false,
    );
  }

  async loginWithPassword(input: {
    deploymentId?: string;
    /** @deprecated Use deploymentId. */
    enterpriseId?: string;
    sessionId?: string;
    username: string;
    password: string;
    /**
     * Optional client identity recorded on the issued session. Servers that
     * predate the field reject the unknown key, so only send it to
     * deployments that advertise session client identity support.
     */
    client?: ClientIdentity;
  }): Promise<AepTokens> {
    const tokens = await this.send<AepTokens>(
      {
        method: HttpMethod.Post,
        path: '/aep/v1/auth/password/login',
        body: asJson({
          deploymentId: input.deploymentId ?? input.enterpriseId,
          username: input.username,
          password: input.password,
          sessionId: input.sessionId ?? this.#sessionId,
          ...(input.client ? {client: input.client} : {}),
        }),
      },
      false,
    );
    await this.#tokenStore.set(tokens);
    this.#sessionId = tokens.sessionId ?? this.#sessionId;
    return tokens;
  }

  startFederatedLogin(input: {
    deploymentId: string;
    methodId: string;
    redirectUri: string;
    codeChallenge: string;
    codeChallengeMethod?: 'S256';
  }): Promise<JsonObject> {
    return this.send(
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
    const tokens = await this.send<AepTokens>(
      {
        method: HttpMethod.Post,
        path: '/aep/v1/auth/exchange',
        body: asJson({...input, sessionId: this.#sessionId}),
      },
      false,
    );
    await this.#tokenStore.set(tokens);
    this.#sessionId = tokens.sessionId ?? this.#sessionId;
    return tokens;
  }

  async changePassword(input: {newPassword: string; currentPassword?: string}): Promise<AepTokens> {
    const tokens = await this.send<AepTokens>({
      method: HttpMethod.Post,
      path: '/aep/v1/auth/password/change',
      body: asJson({
        newPassword: input.newPassword,
        ...(input.currentPassword ? {currentPassword: input.currentPassword} : {}),
      }),
    });
    await this.#tokenStore.set(tokens);
    this.#sessionId = tokens.sessionId ?? this.#sessionId;
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
      await this.send<void>({
        method: HttpMethod.Post,
        path: '/aep/v1/auth/logout',
        body: {refreshToken: tokens.refreshToken},
        responseType: 'empty',
      });
    } finally {
      await this.#tokenStore.clear();
    }
  }

  async getCurrentIdentity(): Promise<CurrentIdentity> {
    const identity = await this.send<CurrentIdentity>({method: HttpMethod.Get, path: '/aep/v1/user/me'});
    const deployment = identity.deployment ?? identity.enterprise;
    return {...identity, deployment, deploymentId: identity.deploymentId ?? deployment.id, enterprise: identity.enterprise ?? deployment};
  }

  /** Canonical user-session identity method. */
  getCurrentUser(): Promise<CurrentIdentity> {
    return this.send({method: HttpMethod.Get, path: '/aep/v1/user/me'});
  }

  activateEnterpriseLicense(input: LicenseActivationRequest = {}): Promise<EntitlementTokenResponse> {
    return this.send({
      method: HttpMethod.Post,
      path: '/aep/v1/user/activation',
      body: asJson(input),
    });
  }

  listCredentialsForUser(): Promise<CredentialList> {
    return this.send({method: HttpMethod.Get, path: '/aep/v1/user/credentials'});
  }

  async resolveCredentialForUser(credentialId: string, purpose: string): Promise<ResolvedCredential> {
    const response = await this.#request<ResolvedCredential>({
      method: HttpMethod.Post,
      path: '/aep/v1/user/credentials/' + segment(credentialId) + '/resolve',
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

  listModels(): Promise<UserModelList> {
    return this.send({method: HttpMethod.Get, path: '/aep/v1/user/models'});
  }

  /** @deprecated Use listModels. */
  listAgentModels(): Promise<AgentModelList> {
    return this.listModels();
  }

  listUserSessions(filters: Query = {}): Promise<JsonObject> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/sessions?${query(filters)}`});
  }

  revokeUserSession(sessionId: string): Promise<void> {
    return this.send({
      method: HttpMethod.Post,
      path: `/aep/v1/admin/sessions/${segment(sessionId)}/revoke`,
      responseType: 'empty',
      retry: false,
    });
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
      path: '/aep/v1/user/skills/manifest',
      headers: etag ? {'If-None-Match': etag} : undefined,
    });
    const responseEtag = response.headers.get('ETag');
    if (response.status === 304) return {notModified: true, etag: responseEtag ?? etag ?? null};
    this.#assertSuccess(response);
    return {notModified: false, etag: responseEtag, manifest: response.data};
  }

  getUserSkillManifest(etag?: string): Promise<SkillManifestResult> {
    return this.#getSkillManifestAt('/aep/v1/user/skills/manifest', etag);
  }

  downloadSkillPackage(skillId: string, version: string): Promise<Uint8Array> {
    return this.send({
      method: HttpMethod.Get,
      path: `/aep/v1/user/skills/${segment(skillId)}/versions/${segment(version)}/package`,
      responseType: 'bytes',
    });
  }

  downloadUserSkillPackage(skillId: string, version: string): Promise<Uint8Array> {
    return this.send({
      method: HttpMethod.Get,
      path: `/aep/v1/user/skills/${segment(skillId)}/versions/${segment(version)}/package`,
      responseType: 'bytes',
    });
  }

  reportSkillSyncResult(result: JsonObject): Promise<void> {
    return this.send({
      method: HttpMethod.Post,
      path: '/aep/v1/user/skills/sync-results',
      body: result,
      responseType: 'empty',
    });
  }

  uploadEventBatch(events: JsonObject[]): Promise<JsonObject> {
    return this.send({
      method: HttpMethod.Post,
      path: '/aep/v1/user/events/batch',
      body: {events},
    });
  }

  uploadUserEventBatch(events: JsonObject[]): Promise<JsonObject> {
    return this.send({method: HttpMethod.Post, path: '/aep/v1/user/events/batch', body: {events}});
  }

  heartbeat(input: JsonObject): Promise<HeartbeatResponse> {
    return this.send({method: HttpMethod.Post, path: '/aep/v1/user/heartbeat', body: input});
  }

  heartbeatUser(input: JsonObject): Promise<HeartbeatResponse> {
    return this.send({method: HttpMethod.Post, path: '/aep/v1/user/heartbeat', body: input});
  }

  listControlEvents(afterCursor?: string, limit = 50): Promise<ControlEventPage> {
    return this.send({
      method: HttpMethod.Get,
      path: `/aep/v1/user/control-events?${query({afterCursor, limit})}`,
    });
  }

  listUserControlEvents(afterCursor?: string, limit = 50): Promise<ControlEventPage> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/user/control-events?${query({afterCursor, limit})}`});
  }

  acknowledgeControlEvent(deliveryId: string, receivedAt: string): Promise<void> {
    return this.send({
      method: HttpMethod.Post,
      path: `/aep/v1/user/control-events/${segment(deliveryId)}/acknowledge`,
      body: {status: 'received', receivedAt},
      responseType: 'empty',
    });
  }

  acknowledgeUserControlEvent(deliveryId: string, receivedAt: string): Promise<void> {
    return this.send({
      method: HttpMethod.Post,
      path: `/aep/v1/user/control-events/${segment(deliveryId)}/acknowledge`,
      body: {status: 'received', receivedAt}, responseType: 'empty',
    });
  }

  reportControlEventResult(deliveryId: string, result: JsonObject): Promise<void> {
    return this.send({
      method: HttpMethod.Post,
      path: `/aep/v1/user/control-events/${segment(deliveryId)}/result`,
      body: result,
      responseType: 'empty',
    });
  }

  reportUserControlEventResult(deliveryId: string, result: JsonObject): Promise<void> {
    return this.send({
      method: HttpMethod.Post,
      path: `/aep/v1/user/control-events/${segment(deliveryId)}/result`,
      body: result, responseType: 'empty',
    });
  }

  protected async send<T>(request: AepRequest, authenticated = true): Promise<T> {
    const response = await this.#request<T>(request, authenticated);
    this.#assertSuccess(response);
    return response.data;
  }

  async #getSkillManifestAt(path: string, etag?: string): Promise<SkillManifestResult> {
    const response = await this.#request<SkillManifest>({
      method: HttpMethod.Get,
      path,
      headers: etag ? {'If-None-Match': etag} : undefined,
    });
    const responseEtag = response.headers.get('ETag');
    if (response.status === 304) return {notModified: true, etag: responseEtag ?? etag ?? null};
    this.#assertSuccess(response);
    return {notModified: false, etag: responseEtag, manifest: response.data};
  }

  async #request<T>(request: AepRequest, authenticated = true): Promise<AepResponse<T>> {
    const headers = {...request.headers, ...this.#sessionHeaders()};
    let usedAccessToken: string | null = null;
    if (authenticated) {
      const tokens = await this.#loadOrRestoreTokens();
      if (tokens) {
        usedAccessToken = tokens.accessToken;
        headers.Authorization = `Bearer ${tokens.accessToken}`;
      }
    }

    const baseUrl = this.#baseFor(request.path);
    let response = await this.#transport.request<T>(baseUrl, {...request, headers});
    if (authenticated && response.status === 401 && request.path !== '/aep/v1/auth/refresh') {
      const tokens = await this.#tokenStore.get();
      if (tokens) {
        const retryTokens = tokens.accessToken === usedAccessToken ? await this.#refresh() : tokens;
        headers.Authorization = `Bearer ${retryTokens.accessToken}`;
        response = await this.#transport.request<T>(baseUrl, {...request, headers});
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
      const response = await this.#transport.request<AepTokens>(this.#baseFor('/aep/v1/auth/refresh'), {
        method: HttpMethod.Post,
        path: '/aep/v1/auth/refresh',
        headers: this.#sessionHeaders(),
        body: this.#sessionBody({refreshToken}),
      });
      if (response.status < 200 || response.status >= 300) {
        await this.#tokenStore.clear();
        throw AepProblem.from(response.status, response.data, response.headers.get('X-Request-ID'));
      }
      await this.#tokenStore.set(response.data);
      this.#sessionId = response.data.sessionId ?? this.#sessionId;
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

  #sessionHeaders(): Record<string, string> {
    return {
      'X-AEP-Protocol-Version': AEP_PROTOCOL_VERSION,
      'X-Request-ID': requestId(),
    };
  }

  #sessionBody(body: Record<string, string>): Record<string, string> {
    return this.#sessionId ? {...body, sessionId: this.#sessionId} : body;
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

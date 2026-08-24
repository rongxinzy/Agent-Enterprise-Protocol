import type { AepCapability, HttpMethod } from './constants.js';
import type {components} from './generated/aep-v1.js';

export type JsonPrimitive = boolean | number | string | null;
export type JsonValue = JsonPrimitive | JsonValue[] | {[key: string]: JsonValue};
export type JsonObject = {[key: string]: JsonValue};

export interface AepTokens {
  accessToken: string;
  refreshToken: string;
  modelAccessToken: string;
  tokenType: 'Bearer';
  expiresIn: number;
  modelAccessExpiresIn: number;
  passwordChangeRequired: boolean;
}

export interface AgentContext {
  agentId: string;
  agentVersion: string;
  platform: 'windows' | 'macos' | 'linux';
}

export interface AepClientOptions extends AgentContext {
  baseUrl: string;
  tokenStore: AepTokenStore;
  transport?: AepTransport;
}

export interface AepTokenStore {
  get(): Promise<AepTokens | null>;
  set(tokens: AepTokens): Promise<void>;
  clear(): Promise<void>;
}

export interface AepRequest {
  method: HttpMethod;
  path: string;
  headers?: Record<string, string>;
  body?: BodyInit | JsonValue;
  timeoutMs?: number;
  retry?: boolean;
  responseType?: 'json' | 'bytes' | 'empty';
}

export interface AepResponse<T> {
  status: number;
  headers: Headers;
  data: T;
}

export interface AepTransport {
  request<T>(baseUrl: string, request: AepRequest): Promise<AepResponse<T>>;
}

export interface ServiceMetadata {
  service: string;
  supportedProtocolVersions: string[];
  capabilities: AepCapability[];
  jwksUri: string;
  modelGateway?: ModelGatewayMetadata;
}

export type CredentialMetadata = components['schemas']['CredentialMetadata'];
export type CredentialList = components['schemas']['CredentialList'];
export type ResolveCredentialRequest = components['schemas']['ResolveCredentialRequest'];
export type ResolvedCredential = components['schemas']['ResolvedCredential'];
export type CredentialCreate = components['schemas']['CredentialCreate'];
export type CredentialPatch = components['schemas']['CredentialPatch'];
export type CredentialRotate = components['schemas']['CredentialRotate'];
export type CredentialAssignment = components['schemas']['CredentialAssignment'];
export type CredentialAssignmentList = components['schemas']['CredentialAssignmentList'];
export type CredentialAssignmentWrite = components['schemas']['CredentialAssignmentWrite'];
export type ModelGatewayMetadata = components['schemas']['ModelGatewayMetadata'];
export type AgentModel = components['schemas']['AgentModel'];
export type AgentModelList = components['schemas']['AgentModelList'];
export type AdminModel = components['schemas']['AdminModel'];
export type AdminModelList = components['schemas']['AdminModelList'];
export type AdminModelWrite = components['schemas']['AdminModelWrite'];
export type AdminModelPatch = components['schemas']['AdminModelPatch'];
export type ModelAssignment = components['schemas']['ModelAssignment'];
export type ModelAssignmentList = components['schemas']['ModelAssignmentList'];
export type ModelAssignmentWrite = components['schemas']['ModelAssignmentWrite'];
export type DataPlaneSecretReference = components['schemas']['DataPlaneSecretReference'];
export type DataPlaneRoute = components['schemas']['DataPlaneRoute'];
export type DataPlaneDesiredStateWrite = components['schemas']['DataPlaneDesiredStateWrite'];
export type DataPlaneDesiredState = components['schemas']['DataPlaneDesiredState'];
export type DataPlaneStatus = components['schemas']['DataPlaneStatus'];

export interface ModelConnection extends ModelGatewayMetadata {
  apiKey: string;
  expiresIn: number;
}

export interface AuthenticationMethod {
  id: string;
  type: 'password' | 'federated';
  protocol?: 'oidc' | 'saml' | 'custom' | null;
  displayName: string;
}

export interface AuthenticationMethods {
  enterprise: {id: string; name: string};
  preferredMethodId?: string | null;
  methods: AuthenticationMethod[];
}

export interface CurrentIdentity {
  user: {id: string; displayName: string; email?: string | null};
  enterprise: {id: string; name: string};
  roles: string[];
  sessionExpiresAt: string;
  passwordChangeRequired: boolean;
}

export interface SkillManifestItem {
  id: string;
  name: string;
  version: string;
  enabled: boolean;
  package: {url: string; sha256: string; size: number};
}

export interface SkillManifest {
  revision: string;
  generatedAt: string;
  skills: SkillManifestItem[];
}

export type SkillManifestResult =
  | {notModified: true; etag: string | null}
  | {notModified: false; etag: string | null; manifest: SkillManifest};

export interface HeartbeatResponse {
  serverTime: string;
  hasPendingControlEvents: boolean;
  controlEventWatermark: string | null;
  nextHeartbeatAfterSeconds: number;
}

export interface ControlEvent {
  deliveryId: string;
  eventId: string;
  cursor: string;
  type: string;
  scope: {type: 'global' | 'organization' | 'user' | 'agent'; id?: string};
  resource?: {type: string; id: string; revision: string};
  task: {type: string};
  createdAt: string;
  expiresAt: string;
}

export interface ControlEventPage {
  items: ControlEvent[];
  nextCursor: string | null;
}

export interface Page<T> {
  items: T[];
  nextCursor?: string | null;
}

export interface PlatformUser {
  id: string;
  enterpriseId: string;
  username: string;
  displayName: string;
  status: 'active' | 'disabled';
  organizationIds?: string[];
  roleIds?: string[];
  createdAt: string;
  updatedAt: string;
}

export interface AdminAgent {
  agentId: string;
  enterpriseId: string;
  userId: string;
  agentVersion: string;
  platform: AgentContext['platform'];
  firstSeenAt: string;
  lastSeenAt: string;
  appliedSkillRevision?: string | null;
  installedSkillIds?: string[];
}

export interface Query {
  [key: string]: boolean | number | string | null | undefined;
}

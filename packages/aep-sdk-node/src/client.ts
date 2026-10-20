import {HttpMethod} from './constants.js';
import {AepResourceClient} from './client-resources.js';
import {asJson, query, segment} from './client-values.js';
import type {
  GatewayCapabilities,
  GatewayNativeResult,
  GatewayHealth,
  GatewayMetricResult,
  GatewayMetricQuery,
  GatewayRequestQuery,
  GatewayLimitWrite,
  ModelPricing,
  ModelPricingWrite,
  GatewayLimit,
  GatewayLimitPage,
  GatewayLimitPublication,
  GatewayLimitStatus,
  GatewayQuota,
  GatewayTestAccess,
  GatewayModelToken,
  GatewayModelTokenRequest,
  AdminControlEvent,
  AdminControlEventPage,
  ComputeAssignment,
  ComputeAssignmentList,
  ComputeTierCatalog,
  DataPlaneDesiredState,
  DataPlaneDesiredStateWrite,
  DataPlanePublishRequest,
  DataPlanePublicationRequest,
  DataPlanePublication,
  DataPlaneStatus,
  DeploymentSettings,
  DeploymentSettingsUpdate,
  JsonObject,
  License,
  LicenseImportRequest,
  LicensePage,
  LicenseRevocation,
  Query,
  TelemetrySearchFilters,
  TelemetryEventPage,
} from './types.js';

export class AepClient extends AepResourceClient {
  getGatewayCapabilities(): Promise<GatewayCapabilities> {
    return this.send({method: HttpMethod.Get, path: '/aep/v1/admin/model-gateway/capabilities'});
  }

  queryGatewayMetrics(filters: GatewayMetricQuery): Promise<GatewayMetricResult> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/model-gateway/metrics?${query(filters)}`});
  }

  getGatewayMonitoringHealth(): Promise<GatewayHealth> {
    return this.send({method: HttpMethod.Get, path: '/aep/v1/admin/model-gateway/health'});
  }

  searchGatewayRequests(filters: GatewayRequestQuery): Promise<GatewayNativeResult> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/model-gateway/requests?${query(filters)}`});
  }

  getGatewayRequest(requestId: string, filters: GatewayRequestQuery): Promise<GatewayNativeResult> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/model-gateway/requests/${segment(requestId)}?${query(filters)}`});
  }

  getModelPricing(modelId: string): Promise<ModelPricing> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/models/${segment(modelId)}/pricing`});
  }

  putModelPricing(modelId: string, input: ModelPricingWrite): Promise<ModelPricing> {
    return this.send({method: HttpMethod.Put, path: `/aep/v1/admin/models/${segment(modelId)}/pricing`, body: asJson(input), retry: false});
  }

  listGatewayLimits(): Promise<GatewayLimitPage> {
    return this.send({method: HttpMethod.Get, path: '/aep/v1/admin/model-gateway/limits'});
  }

  getGatewayLimit(ruleId: string): Promise<GatewayLimit> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/model-gateway/limits/${segment(ruleId)}`});
  }

  putGatewayLimit(ruleId: string, input: GatewayLimitWrite): Promise<GatewayLimit> {
    return this.send({method: HttpMethod.Put, path: `/aep/v1/admin/model-gateway/limits/${segment(ruleId)}`, body: asJson(input), retry: false});
  }

  deleteGatewayLimit(ruleId: string, expectedVersion: number): Promise<void> {
    return this.send({method: HttpMethod.Delete, path: `/aep/v1/admin/model-gateway/limits/${segment(ruleId)}?${query({expectedVersion})}`, responseType: 'empty', retry: false});
  }

  publishGatewayLimits(): Promise<GatewayLimitPublication> {
    return this.send({method: HttpMethod.Post, path: '/aep/v1/admin/model-gateway/limits/publish', retry: false});
  }

  getGatewayLimitsStatus(): Promise<GatewayLimitStatus> {
    return this.send({method: HttpMethod.Get, path: '/aep/v1/admin/model-gateway/limits/status'});
  }

  getGatewayQuota(userId: string): Promise<GatewayQuota> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/model-gateway/quotas/${segment(userId)}`});
  }

  refreshGatewayQuota(userId: string, quota: number): Promise<GatewayQuota> {
    return this.send({method: HttpMethod.Post, path: `/aep/v1/admin/model-gateway/quotas/${segment(userId)}/refresh`, body: {quota}, retry: false});
  }

  changeGatewayQuota(userId: string, value: number): Promise<GatewayQuota> {
    return this.send({method: HttpMethod.Post, path: `/aep/v1/admin/model-gateway/quotas/${segment(userId)}/delta`, body: {value}, retry: false});
  }

  createGatewayTestAccess(modelId: string): Promise<GatewayTestAccess> {
    return this.send({method: HttpMethod.Post, path: `/aep/v1/admin/model-gateway/models/${segment(modelId)}/test-access`, retry: false});
  }

  issueModelGatewayToken(input: GatewayModelTokenRequest): Promise<GatewayModelToken> {
    return this.send({method: HttpMethod.Post, path: '/aep/v1/admin/model-gateway/tokens', body: asJson(input), retry: false});
  }

  putDataPlaneDesiredState(input: DataPlaneDesiredStateWrite): Promise<DataPlaneDesiredState> {
    return this.send({
      method: HttpMethod.Put,
      path: '/aep/v1/admin/data-plane/desired-state',
      body: asJson(input),
    });
  }

  publishDataPlaneRoutes(input: DataPlanePublishRequest = {}): Promise<DataPlaneDesiredState> {
    return this.send({
      method: HttpMethod.Post,
      path: '/aep/v1/admin/data-plane/publish',
      body: asJson(input),
    });
  }

  getDataPlaneStatus(): Promise<DataPlaneStatus> {
    return this.send({method: HttpMethod.Get, path: '/aep/v1/admin/data-plane/status'});
  }

  /** Requires the async publication service. Save/reuse the key across retries; 202 is not Ready. */
  createDataPlanePublication(idempotencyKey: string, input: DataPlanePublicationRequest = {}): Promise<DataPlanePublication> {
    if (typeof idempotencyKey !== 'string' || idempotencyKey.length < 1 || idempotencyKey.length > 128
      || /[^A-Za-z0-9._:-]/.test(idempotencyKey)) {
      throw new TypeError('Idempotency-Key must contain 1-128 ASCII letters, digits or ._:-');
    }
    return this.send({
      method: HttpMethod.Post,
      path: '/aep/v1/admin/data-plane/publications',
      headers: {'Idempotency-Key': idempotencyKey},
      body: asJson({...input}),
      retry: true,
    });
  }

  getDataPlanePublication(operationId: string): Promise<DataPlanePublication> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/data-plane/publications/${segment(operationId)}`});
  }

  /** Replay selected immutable intent; never recapture drafts or roll back older intent. */
  replayDataPlanePublication(operationId: string, idempotencyKey: string): Promise<DataPlanePublication> {
    if (typeof idempotencyKey !== 'string' || idempotencyKey.length < 1 || idempotencyKey.length > 128
      || /[^A-Za-z0-9._:-]/.test(idempotencyKey)) {
      throw new TypeError('Idempotency-Key must contain 1-128 ASCII letters, digits or ._:-');
    }
    return this.send({
      method: HttpMethod.Post,
      path: `/aep/v1/admin/data-plane/publications/${segment(operationId)}/replay`,
      headers: {'Idempotency-Key': idempotencyKey},
      body: {},
      retry: true,
    });
  }

  getDeploymentSettings(): Promise<DeploymentSettings> {
    return this.send({method: HttpMethod.Get, path: '/aep/v1/admin/deployment/settings'});
  }

  updateDeploymentSettings(input: DeploymentSettingsUpdate): Promise<DeploymentSettings> {
    return this.send({
      method: HttpMethod.Put,
      path: '/aep/v1/admin/deployment/settings',
      body: asJson(input),
    });
  }

  getComputeTierCatalog(): Promise<ComputeTierCatalog> {
    return this.send({method: HttpMethod.Get, path: '/aep/v1/admin/compute/tiers'});
  }

  listComputeAssignments(): Promise<ComputeAssignmentList> {
    return this.send({method: HttpMethod.Get, path: '/aep/v1/admin/compute/assignments'});
  }

  updateComputeAssignment(agentId: string, tier: string | null): Promise<ComputeAssignment> {
    return this.send({
      method: HttpMethod.Put,
      path: `/aep/v1/admin/compute/assignments/${segment(agentId)}`,
      body: asJson({tier}),
    });
  }

  createControlEvent(input: JsonObject): Promise<JsonObject> {
    return this.send({method: HttpMethod.Post, path: '/aep/v1/admin/control-events', body: input});
  }

  listAdminControlEvents(filters: Query = {}): Promise<AdminControlEventPage> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/control-events?${query(filters)}`});
  }

  getAdminControlEvent(eventId: string): Promise<AdminControlEvent> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/control-events/${segment(eventId)}`});
  }

  cancelControlEvent(eventId: string): Promise<AdminControlEvent> {
    return this.send({method: HttpMethod.Post, path: `/aep/v1/admin/control-events/${segment(eventId)}/cancel`});
  }

  listControlEventDeliveries(eventId: string, filters: Query = {}): Promise<JsonObject> {
    return this.send({
      method: HttpMethod.Get,
      path: `/aep/v1/admin/control-events/${segment(eventId)}/deliveries?${query(filters)}`,
    });
  }

  searchEvents(filters: TelemetrySearchFilters = {}): Promise<TelemetryEventPage> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/events?${query(filters)}`});
  }

  listLicenses(filters: Query = {}): Promise<LicensePage> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/licenses?${query(filters)}`});
  }

  getLicense(licenseId: string): Promise<License> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/licenses/${segment(licenseId)}`});
  }

  importLicense(input: LicenseImportRequest): Promise<License> {
    return this.send({method: HttpMethod.Post, path: '/aep/v1/admin/licenses/import', body: asJson(input), retry: false});
  }

  revokeLicense(licenseId: string): Promise<LicenseRevocation> {
    return this.send({method: HttpMethod.Post, path: `/aep/v1/admin/licenses/${segment(licenseId)}/revoke`, retry: false});
  }

}

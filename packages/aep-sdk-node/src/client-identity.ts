import {HttpMethod} from './constants.js';
import {AepSessionClient} from './client-session.js';
import {asJson, query, segment} from './client-values.js';
import type {
  AgentDirectoryPage,
  AgentResponse,
  CreateAgentRequest,
  UpdateAgentProfileRequest,
  UpdateAgentProfileResponse,
  CreateIdentitySourceRequest,
  IdentityMapping,
  IdentityMappingPage,
  IdentityMappingResult,
  IdentitySource,
  IdentitySourcePage,
  UpsertIdentityMappingRequest,
  CreateDataScopeRuleRequest,
  DataScopeRule,
  DataScopeRulePage,
  RetrievalContext,
  JsonObject,
  PlatformUser,
  Page,
  Query,
  PermissionPage,
  Role,
  RolePage,
  CreateRoleRequest,
  UpdateRoleRequest,
  Team,
  TeamPage,
  CreateTeamRequest,
  UpdateTeamRequest,
  ReplaceUserRBACRequest,
  UserRBAC,
} from './types.js';

export class AepIdentityClient extends AepSessionClient {
  listUsers(cursor?: string, limit = 50): Promise<Page<PlatformUser>> {
    return this.send({
      method: HttpMethod.Get,
      path: `/aep/v1/admin/users?${query({cursor, limit})}`,
    });
  }

  createUser(input: JsonObject): Promise<PlatformUser> {
    return this.send({method: HttpMethod.Post, path: '/aep/v1/admin/users', body: input});
  }

  importAdminUsers(input: JsonObject): Promise<JsonObject> {
    return this.importUsers(input);
  }

  importUsers(input: JsonObject): Promise<JsonObject> {
    return this.send({method: HttpMethod.Post, path: '/aep/v1/admin/users/import', body: input});
  }

  updateUser(userId: string, input: JsonObject): Promise<PlatformUser> {
    return this.send({
      method: HttpMethod.Patch,
      path: `/aep/v1/admin/users/${segment(userId)}`,
      body: input,
    });
  }

  resetUserPassword(userId: string, input: JsonObject): Promise<void> {
    return this.send({
      method: HttpMethod.Post,
      path: `/aep/v1/admin/users/${segment(userId)}/reset-password`,
      body: input,
      responseType: 'empty',
    });
  }

  listPermissions(): Promise<PermissionPage> {
    return this.send({method: HttpMethod.Get, path: '/aep/v1/admin/permissions'});
  }

  listRoles(filters: Query = {}): Promise<RolePage> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/roles?${query(filters)}`});
  }

  createRole(input: CreateRoleRequest): Promise<Role> {
    return this.send({method: HttpMethod.Post, path: '/aep/v1/admin/roles', body: asJson(input)});
  }

  getRole(roleId: string): Promise<Role> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/roles/${segment(roleId)}`});
  }

  updateRole(roleId: string, input: UpdateRoleRequest): Promise<Role> {
    return this.send({method: HttpMethod.Patch, path: `/aep/v1/admin/roles/${segment(roleId)}`, body: asJson(input)});
  }

  deleteRole(roleId: string): Promise<void> {
    return this.send({method: HttpMethod.Delete, path: `/aep/v1/admin/roles/${segment(roleId)}`, responseType: 'empty'});
  }

  listTeams(filters: Query = {}): Promise<TeamPage> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/teams?${query(filters)}`});
  }

  createTeam(input: CreateTeamRequest): Promise<Team> {
    return this.send({method: HttpMethod.Post, path: '/aep/v1/admin/teams', body: asJson(input)});
  }

  getTeam(teamId: string): Promise<Team> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/teams/${segment(teamId)}`});
  }

  updateTeam(teamId: string, input: UpdateTeamRequest): Promise<Team> {
    return this.send({method: HttpMethod.Patch, path: `/aep/v1/admin/teams/${segment(teamId)}`, body: asJson(input)});
  }

  deleteTeam(teamId: string): Promise<void> {
    return this.send({method: HttpMethod.Delete, path: `/aep/v1/admin/teams/${segment(teamId)}`, responseType: 'empty'});
  }

  replaceUserRBAC(userId: string, input: ReplaceUserRBACRequest): Promise<UserRBAC> {
    return this.send({method: HttpMethod.Put, path: `/aep/v1/admin/users/${segment(userId)}/rbac`, body: asJson(input)});
  }

  listAgents(filters: {cursor?: string; limit?: number; includeEphemeral?: boolean} = {}): Promise<AgentDirectoryPage> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/agents?${query(filters)}`});
  }

  createAgent(input: CreateAgentRequest): Promise<AgentResponse> {
    return this.send({method: HttpMethod.Post, path: '/aep/v1/admin/agents', body: asJson(input)});
  }

  deleteAgent(agentId: string): Promise<void> {
    return this.send({method: HttpMethod.Delete, path: `/aep/v1/admin/agents/${segment(agentId)}`, responseType: 'empty'});
  }

  updateAgentProfile(agentId: string, input: UpdateAgentProfileRequest): Promise<UpdateAgentProfileResponse> {
    return this.send({
      method: HttpMethod.Put,
      path: `/aep/v1/admin/agents/${segment(agentId)}/profile`,
      body: asJson(input),
    });
  }

  listIdentitySources(filters: {cursor?: string; limit?: number} = {}): Promise<IdentitySourcePage> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/identity-sources?${query(filters)}`});
  }

  createIdentitySource(input: CreateIdentitySourceRequest): Promise<IdentitySource> {
    return this.send({method: HttpMethod.Post, path: '/aep/v1/admin/identity-sources', body: asJson(input)});
  }

  listIdentityMappings(
    sourceId: string,
    filters: {subjectType?: IdentityMapping['externalSubjectType']; cursor?: string; limit?: number} = {},
  ): Promise<IdentityMappingPage> {
    return this.send({
      method: HttpMethod.Get,
      path: `/aep/v1/admin/identity-sources/${segment(sourceId)}/mappings?${query(filters)}`,
    });
  }

  upsertIdentityMapping(sourceId: string, input: UpsertIdentityMappingRequest): Promise<IdentityMappingResult> {
    return this.send({
      method: HttpMethod.Put,
      path: `/aep/v1/admin/identity-sources/${segment(sourceId)}/mappings`,
      body: asJson(input),
    });
  }

  deleteIdentityMapping(
    sourceId: string,
    subjectType: IdentityMapping['externalSubjectType'],
    externalId: string,
  ): Promise<void> {
    return this.send({
      method: HttpMethod.Delete,
      path: `/aep/v1/admin/identity-sources/${segment(sourceId)}/mappings/${segment(subjectType)}/${segment(externalId)}`,
      responseType: 'empty',
    });
  }

  listDataScopeRules(filters: {cursor?: string; limit?: number} = {}): Promise<DataScopeRulePage> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/data-scope-rules?${query(filters)}`});
  }

  createDataScopeRule(input: CreateDataScopeRuleRequest): Promise<DataScopeRule> {
    return this.send({method: HttpMethod.Post, path: '/aep/v1/admin/data-scope-rules', body: asJson(input)});
  }

  getDataScopeRule(ruleId: string): Promise<DataScopeRule> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/data-scope-rules/${segment(ruleId)}`});
  }

  deleteDataScopeRule(ruleId: string): Promise<void> {
    return this.send({
      method: HttpMethod.Delete,
      path: `/aep/v1/admin/data-scope-rules/${segment(ruleId)}`,
      responseType: 'empty',
    });
  }

  getDataScopeContext(userId: string): Promise<RetrievalContext> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/data-scope/context?${query({userId})}`});
  }

}

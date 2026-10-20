import {HttpMethod} from './constants.js';
import {AepIdentityClient} from './client-identity.js';
import {asJson, query, segment} from './client-values.js';
import type {
  AdminModel,
  AdminModelList,
  AdminModelPatch,
  AdminModelWrite,
  CredentialAssignment,
  Asset,
  AssetCreate,
  AssetPatch,
  AssetList,
  AssetRuntimeList,
  AssetExecute,
  AssetExecuteResult,
  AssetBinding,
  AssetBindingCreate,
  AssetBindingList,
  AssetTypeList,
  CredentialAssignmentList,
  CredentialAssignmentWrite,
  CredentialCreate,
  CredentialList,
  CredentialMetadata,
  CredentialPatch,
  CredentialRotate,
  DataPlaneDesiredState,
  JsonObject,
  ModelAssignment,
  ModelAssignmentList,
  ModelAssignmentWrite,
  Query,
} from './types.js';

export class AepResourceClient extends AepIdentityClient {
  listSkills(filters: Query = {}): Promise<JsonObject> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/skills?${query(filters)}`});
  }

  createSkill(input: JsonObject): Promise<JsonObject> {
    return this.send({method: HttpMethod.Post, path: '/aep/v1/admin/skills', body: input});
  }

  updateSkill(skillId: string, input: JsonObject): Promise<JsonObject> {
    return this.send({
      method: HttpMethod.Patch,
      path: `/aep/v1/admin/skills/${segment(skillId)}`,
      body: input,
    });
  }

  deleteSkill(skillId: string, options: {force?: boolean} = {}): Promise<void> {
    const search = query({force: options.force || undefined});
    return this.send({
      method: HttpMethod.Delete,
      path: `/aep/v1/admin/skills/${segment(skillId)}${search ? `?${search}` : ''}`,
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
    return this.send({
      method: HttpMethod.Post,
      path: `/aep/v1/admin/skills/${segment(skillId)}/versions`,
      body: form,
    });
  }

  publishSkillVersion(skillId: string, version: string): Promise<JsonObject> {
    return this.send({
      method: HttpMethod.Post,
      path: `/aep/v1/admin/skills/${segment(skillId)}/versions/${segment(version)}/publish`,
    });
  }

  deleteSkillVersion(skillId: string, version: string): Promise<void> {
    return this.send({
      method: HttpMethod.Delete,
      path: `/aep/v1/admin/skills/${segment(skillId)}/versions/${segment(version)}`,
      responseType: 'empty',
    });
  }

  listSkillAssignments(filters: Query = {}): Promise<JsonObject> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/skill-assignments?${query(filters)}`});
  }

  createSkillAssignment(input: JsonObject): Promise<JsonObject> {
    return this.send({
      method: HttpMethod.Post,
      path: '/aep/v1/admin/skill-assignments',
      body: input,
    });
  }

  updateSkillAssignment(assignmentId: string, input: JsonObject): Promise<JsonObject> {
    return this.send({
      method: HttpMethod.Patch,
      path: `/aep/v1/admin/skill-assignments/${segment(assignmentId)}`,
      body: input,
    });
  }
  deleteSkillAssignment(assignmentId: string): Promise<void> {
    return this.send({
      method: HttpMethod.Delete,
      path: `/aep/v1/admin/skill-assignments/${segment(assignmentId)}`,
      responseType: 'empty',
    });
  }

  listCredentials(filters: Query = {}): Promise<CredentialList> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/credentials?${query(filters)}`});
  }

  createCredential(input: CredentialCreate): Promise<CredentialMetadata> {
    return this.send({method: HttpMethod.Post, path: '/aep/v1/admin/credentials', body: asJson(input)});
  }

  getCredential(credentialId: string): Promise<CredentialMetadata> {
    return this.send({method: HttpMethod.Get, path: '/aep/v1/admin/credentials/' + segment(credentialId)});
  }

  updateCredential(credentialId: string, input: CredentialPatch): Promise<CredentialMetadata> {
    return this.send({
      method: HttpMethod.Patch,
      path: '/aep/v1/admin/credentials/' + segment(credentialId),
      body: asJson(input),
    });
  }

  rotateCredential(credentialId: string, input: CredentialRotate): Promise<CredentialMetadata> {
    return this.send({
      method: HttpMethod.Post,
      path: '/aep/v1/admin/credentials/' + segment(credentialId) + '/rotate',
      body: asJson(input),
      retry: false,
    });
  }

  deleteCredential(credentialId: string): Promise<void> {
    return this.send({
      method: HttpMethod.Delete,
      path: '/aep/v1/admin/credentials/' + segment(credentialId),
      responseType: 'empty',
    });
  }

  listCredentialAssignments(filters: Query = {}): Promise<CredentialAssignmentList> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/credential-assignments?${query(filters)}`});
  }

  createCredentialAssignment(input: CredentialAssignmentWrite): Promise<CredentialAssignment> {
    return this.send({
      method: HttpMethod.Post,
      path: '/aep/v1/admin/credential-assignments',
      body: asJson(input),
    });
  }

  listAssetTypes(): Promise<AssetTypeList> {
    return this.send({method: HttpMethod.Get, path: '/aep/v1/admin/asset-types'});
  }

  listAssets(filters: Query = {}): Promise<AssetList> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/assets?${query(filters)}`});
  }

  createAsset(input: AssetCreate): Promise<Asset> {
    return this.send({method: HttpMethod.Post, path: '/aep/v1/admin/assets', body: asJson(input)});
  }

  getAsset(assetId: string): Promise<Asset> {
    return this.send({method: HttpMethod.Get, path: '/aep/v1/admin/assets/' + segment(assetId)});
  }

  updateAsset(assetId: string, input: AssetPatch): Promise<Asset> {
    return this.send({
      method: HttpMethod.Patch,
      path: '/aep/v1/admin/assets/' + segment(assetId),
      body: asJson(input),
    });
  }

  listRuntimeAssets(subjectType: string, subjectId: string): Promise<AssetRuntimeList> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/assets/runtime?${query({subjectType, subjectId})}`});
  }

  executeAsset(assetId: string, input: AssetExecute): Promise<AssetExecuteResult> {
    return this.send({
      method: HttpMethod.Post,
      path: '/aep/v1/admin/assets/' + segment(assetId) + '/execute',
      body: asJson(input),
      retry: false,
    });
  }

  detectAsset(assetId: string): Promise<Asset> {
    return this.send({method: HttpMethod.Post, path: '/aep/v1/admin/assets/' + segment(assetId) + '/detect'});
  }

  deleteAsset(assetId: string): Promise<void> {
    return this.send({
      method: HttpMethod.Delete,
      path: '/aep/v1/admin/assets/' + segment(assetId),
      responseType: 'empty',
    });
  }

  listAssetBindings(assetId: string, filters: Query = {}): Promise<AssetBindingList> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/assets/${segment(assetId)}/bindings?${query(filters)}`});
  }

  createAssetBinding(assetId: string, input: AssetBindingCreate): Promise<AssetBinding> {
    return this.send({
      method: HttpMethod.Post,
      path: '/aep/v1/admin/assets/' + segment(assetId) + '/bindings',
      body: asJson(input),
    });
  }

  deleteAssetBinding(assetId: string, bindingId: string): Promise<void> {
    return this.send({
      method: HttpMethod.Delete,
      path: '/aep/v1/admin/assets/' + segment(assetId) + '/bindings/' + segment(bindingId),
      responseType: 'empty',
    });
  }

  deleteCredentialAssignment(assignmentId: string): Promise<void> {
    return this.send({
      method: HttpMethod.Delete,
      path: '/aep/v1/admin/credential-assignments/' + segment(assignmentId),
      responseType: 'empty',
    });
  }

  listAdminModels(filters: Query = {}): Promise<AdminModelList> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/models?${query(filters)}`});
  }

  createModel(input: AdminModelWrite): Promise<AdminModel> {
    return this.send({method: HttpMethod.Post, path: '/aep/v1/admin/models', body: asJson(input)});
  }

  getModel(modelId: string): Promise<AdminModel> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/models/${segment(modelId)}`});
  }

  updateModel(modelId: string, input: AdminModelPatch): Promise<AdminModel> {
    return this.send({
      method: HttpMethod.Patch,
      path: `/aep/v1/admin/models/${segment(modelId)}`,
      body: asJson(input),
    });
  }

  deleteModel(modelId: string): Promise<void> {
    return this.send({
      method: HttpMethod.Delete,
      path: `/aep/v1/admin/models/${segment(modelId)}`,
      responseType: 'empty',
    });
  }

  listModelAssignments(filters: Query = {}): Promise<ModelAssignmentList> {
    return this.send({method: HttpMethod.Get, path: `/aep/v1/admin/model-assignments?${query(filters)}`});
  }

  createModelAssignment(input: ModelAssignmentWrite): Promise<ModelAssignment> {
    return this.send({
      method: HttpMethod.Post,
      path: '/aep/v1/admin/model-assignments',
      body: asJson(input),
    });
  }

  deleteModelAssignment(assignmentId: string): Promise<void> {
    return this.send({
      method: HttpMethod.Delete,
      path: `/aep/v1/admin/model-assignments/${segment(assignmentId)}`,
      responseType: 'empty',
    });
  }

  getDataPlaneDesiredState(): Promise<DataPlaneDesiredState> {
    return this.send({method: HttpMethod.Get, path: '/aep/v1/admin/data-plane/desired-state'});
  }

}

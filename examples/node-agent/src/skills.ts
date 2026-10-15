import {createHash} from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';

import type {AepClient, SkillManifest, SkillManifestItem} from '@aep/sdk-node';
import yauzl from 'yauzl';

import type {AgentState} from './state.js';

// Reported through `reportSkillSyncResult`. `installed`, `updated` and
// `unchanged` count as installed server side; `removed` and `failed` do not.
export type SkillSyncItem = {
  skillId: string;
  status: 'installed' | 'updated' | 'unchanged' | 'removed' | 'failed';
  errorCode?: string;
  message?: string;
};

export type SkillSyncOutcome = {
  revision: string;
  status: 'succeeded' | 'partial';
  items: SkillSyncItem[];
};

export class SkillReconciler {
  constructor(
    private readonly client: AepClient,
    private readonly state: AgentState,
    private readonly managedRoot: string,
  ) {}

  // Only a manifest that arrived complete reaches the local installation, and
  // every Skill is then applied on its own: a package that fails to install is
  // reported per Skill without holding back the revocations of the same pass.
  async reconcile(): Promise<SkillSyncOutcome> {
    const etag = this.state.getValue('skill_etag') ?? undefined;
    const result = await this.client.getUserSkillManifest(etag);
    if (result.notModified) {
      return {
        revision: this.state.getValue('skill_revision') ?? '',
        status: 'succeeded',
        items: this.state.managedSkills().map((skill): SkillSyncItem => ({skillId: skill.skillId, status: 'unchanged'})),
      };
    }

    const skills = listedSkills(result.manifest);
    fs.mkdirSync(this.managedRoot, {recursive: true});
    const desired = new Set(skills.map(skill => skill.id));
    const items: SkillSyncItem[] = [];
    const failures: string[] = [];
    for (const skill of skills) {
      const installed = this.state.managedSkills().find(item => item.skillId === skill.id);
      if (installed?.version === skill.version && installed.sha256 === skill.package.sha256) {
        items.push({skillId: skill.id, status: 'unchanged'});
        continue;
      }
      try {
        await this.install(skill);
        items.push({skillId: skill.id, status: installed ? 'updated' : 'installed'});
      } catch (error) {
        // The Skill stays as it was - the manifest lists it, so it must not be
        // revoked, and the failure is reported instead of aborting the pass.
        const message = error instanceof Error ? error.message : String(error);
        items.push({skillId: skill.id, status: 'failed', errorCode: 'SKILL_INSTALL_FAILED', message});
        failures.push(`install ${skill.id}: ${message}`);
      }
    }
    for (const installed of this.state.managedSkills()) {
      if (desired.has(installed.skillId)) continue;
      try {
        removeDirectory(installed.path);
        this.state.removeManagedSkill(installed.skillId);
        items.push({skillId: installed.skillId, status: 'removed'});
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        items.push({skillId: installed.skillId, status: 'failed', errorCode: 'SKILL_REMOVE_FAILED', message});
        failures.push(`remove ${installed.skillId}: ${message}`);
      }
    }
    if (failures.length === 0) {
      this.state.setValue('skill_revision', result.manifest.revision);
      if (result.etag) this.state.setValue('skill_etag', result.etag);
      return {revision: result.manifest.revision, status: 'succeeded', items};
    }
    // The stored etag and revision stay behind so the next pass refetches the
    // full manifest and retries the Skills that failed here.
    return {revision: result.manifest.revision, status: 'partial', items};
  }

  private async install(skill: SkillManifestItem): Promise<void> {
    const archive = await this.client.downloadUserSkillPackage(skill.id, skill.version);
    const actualHash = createHash('sha256').update(archive).digest('hex');
    if (actualHash !== skill.package.sha256) {
      throw new Error(`Skill ${skill.id} checksum mismatch`);
    }
    const staging = path.join(this.managedRoot, `.staging-${skill.id}-${crypto.randomUUID()}`);
    const target = path.join(this.managedRoot, safeName(skill.id));
    const backup = path.join(this.managedRoot, `.backup-${skill.id}-${crypto.randomUUID()}`);
    fs.mkdirSync(staging, {recursive: true});
    try {
      await extractZipSafely(archive, staging);
      if (!fs.existsSync(path.join(staging, 'SKILL.md'))) throw new Error(`Skill ${skill.id} package does not contain root SKILL.md`);
      if (fs.existsSync(target)) fs.renameSync(target, backup);
      fs.renameSync(staging, target);
      removeDirectory(backup);
      this.state.setManagedSkill(skill.id, skill.version, skill.package.sha256, target);
    } catch (error) {
      removeDirectory(staging);
      if (!fs.existsSync(target) && fs.existsSync(backup)) fs.renameSync(backup, target);
      throw error;
    }
  }
}

// The manifest is authoritative: a Skill it does not list is revoked. That is
// only sound for a manifest that actually listed the Skills, so a truncated or
// otherwise broken payload is rejected before anything is removed.
function listedSkills(manifest: SkillManifest): SkillManifestItem[] {
  if (!Array.isArray(manifest?.skills)) throw new Error('The Skill manifest did not list any Skills');
  return manifest.skills;
}

export function extractZipSafely(archive: Uint8Array, destination: string): Promise<void> {
  return new Promise((resolve, reject) => {
    yauzl.fromBuffer(Buffer.from(archive), {lazyEntries: true}, (openError, zip) => {
      if (openError || !zip) return reject(openError ?? new Error('ZIP could not be opened'));
      const fail = (error: Error): void => { zip.close(); reject(error); };
      zip.on('error', fail);
      zip.on('entry', entry => {
        const normalized = entry.fileName.replaceAll('\\', '/');
        const segments = normalized.split('/').filter(Boolean);
        const fileType = (entry.externalFileAttributes >>> 16) & 0o170000;
        if (normalized.startsWith('/') || /^[A-Za-z]:/.test(normalized) || segments.includes('..') || fileType === 0o120000) {
          fail(new Error(`Unsafe ZIP entry: ${entry.fileName}`));
          return;
        }
        const output = path.resolve(destination, ...segments);
        if (!output.startsWith(path.resolve(destination) + path.sep) && output !== path.resolve(destination)) {
          fail(new Error(`ZIP entry escaped destination: ${entry.fileName}`));
          return;
        }
        if (entry.fileName.endsWith('/')) {
          fs.mkdirSync(output, {recursive: true});
          zip.readEntry();
          return;
        }
        fs.mkdirSync(path.dirname(output), {recursive: true});
        zip.openReadStream(entry, (streamError, stream) => {
          if (streamError || !stream) return fail(streamError ?? new Error('ZIP entry could not be read'));
          const writer = fs.createWriteStream(output, {flags: 'wx'});
          writer.on('error', fail);
          writer.on('close', () => zip.readEntry());
          stream.pipe(writer);
        });
      });
      zip.on('end', resolve);
      zip.readEntry();
    });
  });
}

function safeName(value: string): string {
  if (!/^[A-Za-z0-9._-]+$/.test(value)) throw new Error(`Unsafe Skill identifier: ${value}`);
  return value;
}

function removeDirectory(directory: string): void {
  if (fs.existsSync(directory)) fs.rmSync(directory, {recursive: true, force: true});
}

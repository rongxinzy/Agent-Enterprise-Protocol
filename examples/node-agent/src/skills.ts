import {createHash} from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';

import type {AepClient, SkillManifest, SkillManifestItem} from '@aep/sdk-node';
import yauzl from 'yauzl';

import type {AgentState} from './state.js';

export class SkillReconciler {
  constructor(
    private readonly client: AepClient,
    private readonly state: AgentState,
    private readonly managedRoot: string,
  ) {}

  async reconcile(): Promise<{revision: string; items: Array<{skillId: string; status: string}>}> {
    const etag = this.state.getValue('skill_etag') ?? undefined;
    const result = await this.client.getSkillManifest(etag);
    if (result.notModified) {
      return {revision: this.state.getValue('skill_revision') ?? '', items: this.state.managedSkills().map(skill => ({skillId: skill.skillId, status: 'unchanged'}))};
    }

    fs.mkdirSync(this.managedRoot, {recursive: true});
    const desired = new Set(result.manifest.skills.map(skill => skill.id));
    const items: Array<{skillId: string; status: string}> = [];
    for (const skill of result.manifest.skills) {
      const installed = this.state.managedSkills().find(item => item.skillId === skill.id);
      if (installed?.version === skill.version && installed.sha256 === skill.package.sha256) {
        items.push({skillId: skill.id, status: 'unchanged'});
        continue;
      }
      await this.install(skill);
      items.push({skillId: skill.id, status: installed ? 'updated' : 'installed'});
    }
    for (const installed of this.state.managedSkills()) {
      if (desired.has(installed.skillId)) continue;
      removeDirectory(installed.path);
      this.state.removeManagedSkill(installed.skillId);
      items.push({skillId: installed.skillId, status: 'removed'});
    }
    this.state.setValue('skill_revision', result.manifest.revision);
    if (result.etag) this.state.setValue('skill_etag', result.etag);
    return {revision: result.manifest.revision, items};
  }

  private async install(skill: SkillManifestItem): Promise<void> {
    const archive = await this.client.downloadSkillPackage(skill.id, skill.version);
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

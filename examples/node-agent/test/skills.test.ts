import {createHash} from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

import {afterEach, expect, test, vi} from 'vitest';
import yazl from 'yazl';

import type {AepClient} from '@aep/sdk-node';

import {SkillReconciler, extractZipSafely} from '../src/skills.js';
import {AgentState} from '../src/state.js';

const directories: string[] = [];

afterEach(() => {
  for (const directory of directories.splice(0)) fs.rmSync(directory, {recursive: true, force: true});
});

test('extracts a valid Skill package', async () => {
  const destination = tempDirectory();
  const archive = await zip([['SKILL.md', '# Demo'], ['references/info.txt', 'reference']]);
  await extractZipSafely(archive, destination);
  expect(fs.readFileSync(path.join(destination, 'SKILL.md'), 'utf8')).toBe('# Demo');
});

test('rejects ZIP path traversal', async () => {
  const destination = tempDirectory();
  const archive = await zip([['aa/escaped.txt', 'bad']]);
  const malicious = Buffer.from(archive);
  replaceAll(malicious, Buffer.from('aa/escaped.txt'), Buffer.from('../escaped.txt'));
  await expect(extractZipSafely(malicious, destination)).rejects.toThrow(/Unsafe ZIP entry|invalid relative path/);
  expect(fs.existsSync(path.join(destination, '..', 'escaped.txt'))).toBe(false);
});

test('revokes a withdrawn Skill even when another Skill fails to install', async () => {
  const directory = tempDirectory();
  const state = new AgentState(path.join(directory, 'state.sqlite'));
  const managedRoot = path.join(directory, 'managed-skills');
  const revokedPath = installManagedSkill(state, managedRoot, 'revoked', '1.0.0', 'a'.repeat(64));
  const client = manifestClient(manifest([{id: 'broken', sha256: 'b'.repeat(64)}]), async () => {
    throw new Error('object storage unavailable');
  });
  try {
    const reconciler = new SkillReconciler(client, state, managedRoot);
    const result = await reconciler.reconcile();
    expect(result.status).toBe('partial');
    expect(result.items).toEqual([
      {skillId: 'broken', status: 'failed', errorCode: 'SKILL_INSTALL_FAILED', message: 'object storage unavailable'},
      {skillId: 'revoked', status: 'removed'},
    ]);
    expect(fs.existsSync(revokedPath)).toBe(false);
    expect(state.managedSkills()).toEqual([]);

    // The failed install is not stored as the applied revision, so the next
    // pass refetches the manifest and retries it.
    expect(state.getValue('skill_revision')).toBeNull();
    const retry = await reconciler.reconcile();
    expect(retry.status).toBe('partial');
    expect(retry.items).toEqual([
      {skillId: 'broken', status: 'failed', errorCode: 'SKILL_INSTALL_FAILED', message: 'object storage unavailable'},
    ]);
    expect(fs.existsSync(revokedPath)).toBe(false);
  } finally {
    state.close();
  }
});

test('reports a checksum mismatch without dropping the installed Skill', async () => {
  const directory = tempDirectory();
  const state = new AgentState(path.join(directory, 'state.sqlite'));
  const managedRoot = path.join(directory, 'managed-skills');
  const installedPath = installManagedSkill(state, managedRoot, 'demo', '1.0.0', 'a'.repeat(64));
  const archive = await zip([['SKILL.md', '# Demo']]);
  const client = manifestClient(manifest([{id: 'demo', version: '2.0.0', sha256: '0'.repeat(64)}]), async () => archive);
  try {
    const result = await new SkillReconciler(client, state, managedRoot).reconcile();
    expect(result.status).toBe('partial');
    expect(result.items).toEqual([
      {skillId: 'demo', status: 'failed', errorCode: 'SKILL_INSTALL_FAILED', message: 'Skill demo checksum mismatch'},
    ]);
    // The manifest lists the Skill, so a rejected package must not be read as a
    // withdrawal: the previous version stays installed and tracked.
    expect(fs.readFileSync(path.join(installedPath, 'SKILL.md'), 'utf8')).toBe('# demo 1.0.0');
    expect(state.managedSkills()).toEqual([{skillId: 'demo', version: '1.0.0', sha256: 'a'.repeat(64), path: installedPath}]);
    expect(fs.readdirSync(managedRoot)).toEqual(['demo']);
  } finally {
    state.close();
  }
});

test('keeps the installed Skills when the manifest cannot be fetched', async () => {
  const directory = tempDirectory();
  const state = new AgentState(path.join(directory, 'state.sqlite'));
  const managedRoot = path.join(directory, 'managed-skills');
  const installedPath = installManagedSkill(state, managedRoot, 'revoked', '1.0.0', 'a'.repeat(64));
  const client = {
    getUserSkillManifest: async () => {
      throw new Error('network unreachable');
    },
    downloadUserSkillPackage: async () => {
      throw new Error('the package must not be downloaded');
    },
  } as unknown as AepClient;
  try {
    await expect(new SkillReconciler(client, state, managedRoot).reconcile()).rejects.toThrow('network unreachable');
    expect(fs.existsSync(installedPath)).toBe(true);
    expect(state.managedSkills().map(skill => skill.skillId)).toEqual(['revoked']);
  } finally {
    state.close();
  }
});

test('keeps the installed Skills when the manifest is not a complete list', async () => {
  const directory = tempDirectory();
  const state = new AgentState(path.join(directory, 'state.sqlite'));
  const managedRoot = path.join(directory, 'managed-skills');
  const installedPath = installManagedSkill(state, managedRoot, 'revoked', '1.0.0', 'a'.repeat(64));
  const client = manifestClient({revision: '2', generatedAt: new Date().toISOString()}, async () => {
    throw new Error('the package must not be downloaded');
  });
  try {
    await expect(new SkillReconciler(client, state, managedRoot).reconcile()).rejects.toThrow('did not list any Skills');
    expect(fs.existsSync(installedPath)).toBe(true);
    expect(state.managedSkills().map(skill => skill.skillId)).toEqual(['revoked']);
  } finally {
    state.close();
  }
});

test('removes a Skill that a complete manifest no longer lists', async () => {
  const directory = tempDirectory();
  const state = new AgentState(path.join(directory, 'state.sqlite'));
  const managedRoot = path.join(directory, 'managed-skills');
  const installedPath = installManagedSkill(state, managedRoot, 'revoked', '1.0.0', 'a'.repeat(64));
  const client = manifestClient(manifest([]), async () => {
    throw new Error('the package must not be downloaded');
  });
  try {
    const result = await new SkillReconciler(client, state, managedRoot).reconcile();
    expect(result).toEqual({revision: '2', status: 'succeeded', items: [{skillId: 'revoked', status: 'removed'}]});
    expect(fs.existsSync(installedPath)).toBe(false);
    expect(state.managedSkills()).toEqual([]);
    expect(state.getValue('skill_revision')).toBe('2');
    expect(state.getValue('skill_etag')).toBe('"revision-2"');
  } finally {
    state.close();
  }
});

test('applies a complete manifest and reports every outcome', async () => {
  const directory = tempDirectory();
  const state = new AgentState(path.join(directory, 'state.sqlite'));
  const managedRoot = path.join(directory, 'managed-skills');
  installManagedSkill(state, managedRoot, 'keep', '1.0.0', 'a'.repeat(64));
  const bumpedPath = installManagedSkill(state, managedRoot, 'bump', '1.0.0', 'b'.repeat(64));
  const removedPath = installManagedSkill(state, managedRoot, 'gone', '1.0.0', 'c'.repeat(64));
  const bumped = await zip([['SKILL.md', '# bump 2.0.0']]);
  const fresh = await zip([['SKILL.md', '# fresh 1.0.0']]);
  const client = manifestClient(
    manifest([
      {id: 'bump', version: '2.0.0', sha256: createHash('sha256').update(bumped).digest('hex')},
      {id: 'fresh', sha256: createHash('sha256').update(fresh).digest('hex')},
      {id: 'keep', sha256: 'a'.repeat(64)},
    ]),
    async skillId => (skillId === 'bump' ? bumped : fresh),
  );
  try {
    const result = await new SkillReconciler(client, state, managedRoot).reconcile();
    expect(result).toEqual({
      revision: '2',
      status: 'succeeded',
      items: [
        {skillId: 'bump', status: 'updated'},
        {skillId: 'fresh', status: 'installed'},
        {skillId: 'keep', status: 'unchanged'},
        {skillId: 'gone', status: 'removed'},
      ],
    });
    expect(fs.readFileSync(path.join(bumpedPath, 'SKILL.md'), 'utf8')).toBe('# bump 2.0.0');
    expect(fs.existsSync(removedPath)).toBe(false);
    expect(state.managedSkills().map(skill => `${skill.skillId}@${skill.version}`)).toEqual(['bump@2.0.0', 'fresh@1.0.0', 'keep@1.0.0']);
    expect(state.getValue('skill_revision')).toBe('2');
    expect(state.getValue('skill_etag')).toBe('"revision-2"');
  } finally {
    state.close();
  }
});

test('reports a revocation that could not be recorded and retries it', async () => {
  const directory = tempDirectory();
  const state = new AgentState(path.join(directory, 'state.sqlite'));
  const managedRoot = path.join(directory, 'managed-skills');
  installManagedSkill(state, managedRoot, 'revoked', '1.0.0', 'a'.repeat(64));
  const client = manifestClient(manifest([]), async () => {
    throw new Error('the package must not be downloaded');
  });
  const reconcile = vi.spyOn(state, 'removeManagedSkill').mockImplementation(() => {
    throw new Error('managed_skills is locked');
  });
  try {
    const reconciler = new SkillReconciler(client, state, managedRoot);
    const result = await reconciler.reconcile();
    expect(result.status).toBe('partial');
    expect(result.items).toEqual([
      {skillId: 'revoked', status: 'failed', errorCode: 'SKILL_REMOVE_FAILED', message: 'managed_skills is locked'},
    ]);
    // The Skill is still tracked, so the failure is retried instead of being
    // reported as a completed revocation.
    expect(state.managedSkills().map(skill => skill.skillId)).toEqual(['revoked']);
    expect(state.getValue('skill_revision')).toBeNull();
    reconcile.mockRestore();
    expect((await reconciler.reconcile()).items).toEqual([{skillId: 'revoked', status: 'removed'}]);
    expect(state.managedSkills()).toEqual([]);
  } finally {
    state.close();
  }
});

interface ManifestEntry {
  id: string;
  version?: string;
  sha256?: string;
}

function manifest(entries: ManifestEntry[]): unknown {
  return {
    revision: '2',
    generatedAt: new Date().toISOString(),
    skills: entries.map(entry => ({
      id: entry.id,
      name: entry.id,
      version: entry.version ?? '1.0.0',
      enabled: true,
      package: {url: `/${entry.id}.zip`, sha256: entry.sha256 ?? 'f'.repeat(64), size: 1},
    })),
  };
}

function manifestClient(payload: unknown, download: (skillId: string, version: string) => Promise<Uint8Array>): AepClient {
  return {
    getUserSkillManifest: async () => ({notModified: false, etag: '"revision-2"', manifest: payload}),
    downloadUserSkillPackage: download,
  } as unknown as AepClient;
}

function installManagedSkill(state: AgentState, managedRoot: string, skillId: string, version: string, sha256: string): string {
  const skillPath = path.join(managedRoot, skillId);
  fs.mkdirSync(skillPath, {recursive: true});
  fs.writeFileSync(path.join(skillPath, 'SKILL.md'), `# ${skillId} ${version}`);
  state.setManagedSkill(skillId, version, sha256, skillPath);
  return skillPath;
}

function tempDirectory(): string {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'aep-agent-skill-'));
  directories.push(directory);
  return directory;
}

function zip(files: Array<[string, string]>): Promise<Uint8Array> {
  return new Promise((resolve, reject) => {
    const archive = new yazl.ZipFile();
    const chunks: Buffer[] = [];
    archive.outputStream.on('data', chunk => chunks.push(Buffer.from(chunk)));
    archive.outputStream.on('error', reject);
    archive.outputStream.on('end', () => resolve(new Uint8Array(Buffer.concat(chunks))));
    for (const [name, content] of files) archive.addBuffer(Buffer.from(content), name);
    archive.end();
  });
}
function replaceAll(buffer: Buffer, search: Buffer, replacement: Buffer): void {
  let offset = 0;
  while ((offset = buffer.indexOf(search, offset)) >= 0) {
    replacement.copy(buffer, offset);
    offset += replacement.length;
  }
}

import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

import {afterEach, expect, test} from 'vitest';
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

test('does not install a Skill when its checksum mismatches', async () => {
  const directory = tempDirectory();
  const archive = await zip([['SKILL.md', '# Demo']]);
  const state = new AgentState(path.join(directory, 'state.sqlite'));
  const client = {
    getSkillManifest: async () => ({notModified: false, etag: '"revision-1"', manifest: {revision: '1', generatedAt: new Date().toISOString(), skills: [{id: 'demo', name: 'Demo', version: '1.0.0', enabled: true, package: {url: '/demo.zip', sha256: '0'.repeat(64), size: archive.byteLength}}]}}),
    downloadSkillPackage: async () => archive,
  } as unknown as AepClient;
  try {
    const managedRoot = path.join(directory, 'managed-skills');
    await expect(new SkillReconciler(client, state, managedRoot).reconcile()).rejects.toThrow('checksum mismatch');
    expect(state.managedSkills()).toEqual([]);
    expect(fs.existsSync(path.join(managedRoot, 'demo'))).toBe(false);
  } finally {
    state.close();
  }
});

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

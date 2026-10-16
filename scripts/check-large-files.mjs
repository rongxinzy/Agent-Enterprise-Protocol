import {readFileSync} from 'node:fs';
import {statSync} from 'node:fs';
import {execFileSync} from 'node:child_process';
import path from 'node:path';

const maxBytes = Number(process.env.AEP_LARGE_FILE_MAX_BYTES ?? 2 * 1024 * 1024);
const allowlistPath = path.resolve('.github/large-file-allowlist.txt');
const allowlist = new Set(
  readFileSync(allowlistPath, 'utf8')
    .split('\n')
    .map(line => line.replace(/#.*$/, '').trim())
    .filter(Boolean),
);

const files = execFileSync('git', ['ls-files', '-z'], {encoding: 'utf8'})
  .slice(0, -1)
  .split('\0');
const violations = [];
for (const relativePath of files) {
  if (allowlist.has(relativePath)) continue;
  const size = statSync(relativePath).size;
  if (size > maxBytes) violations.push({relativePath, size});
}
if (violations.length) {
  for (const {relativePath, size} of violations.sort((a, b) => b.size - a.size)) {
    console.error(`::error file=${relativePath}::${relativePath} is ${size} bytes (limit ${maxBytes})`);
  }
  process.exit(1);
}
console.log(`Large-file check passed (${files.length} tracked files, limit ${maxBytes} bytes).`);

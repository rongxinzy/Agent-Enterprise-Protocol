import {existsSync, readFileSync, statSync} from 'node:fs';
import {execFileSync} from 'node:child_process';
import path from 'node:path';

const lineLimit = Number(process.env.AEP_SOURCE_LINE_LIMIT ?? 1000);
const maxBytes = Number(process.env.AEP_LARGE_FILE_MAX_BYTES ?? 2 * 1024 * 1024);
const baselinePath = path.resolve('.github/source-line-baseline.json');
const baseline = JSON.parse(readFileSync(baselinePath, 'utf8'));

const sourcePattern =
  /\.(go|ts|tsx|js|mjs|cjs|py|sql|sh)$|\.openapi\.yaml$|openapi\/[^/]+\.yaml$/;
const skipPattern =
  /(?:^|\/)(generated|vendor)(?:\/|$)|\.snap$|\.bundle\.yaml$|lock(?:\.json|-file)?$|openapi\/dist\//;

const files = execFileSync('git', ['ls-files', '-z'], {encoding: 'utf8'})
  .slice(0, -1)
  .split('\0');

function countLines(relativePath) {
  const content = readFileSync(relativePath, 'utf8');
  if (!content) return 0;
  let lines = content.split('\n').length;
  if (content.endsWith('\n')) lines--;
  return lines;
}

const errors = [];
for (const relativePath of files) {
  if (!existsSync(relativePath) || skipPattern.test(relativePath) || !sourcePattern.test(relativePath)) continue;
  const lines = countLines(relativePath);
  const cap = baseline[relativePath];
  if (cap !== undefined) {
    if (lines > cap) {
      errors.push(`${relativePath}: ${lines} lines exceeds baseline cap ${cap} (must shrink, not grow)`);
    } else if (lines <= lineLimit) {
      errors.push(`${relativePath}: ${lines} lines is under ${lineLimit}; remove it from ${path.basename(baselinePath)}`);
    }
    continue;
  }
  if (lines > lineLimit) {
    errors.push(`${relativePath}: ${lines} lines exceeds ${lineLimit} (add a baseline cap only for existing debt, then shrink)`);
  }
}

for (const relativePath of files) {
  if (!existsSync(relativePath) || skipPattern.test(relativePath)) continue;
  const size = statSync(relativePath).size;
  if (size > maxBytes) {
    errors.push(`${relativePath}: ${size} bytes exceeds ${maxBytes} byte limit`);
  }
}

if (errors.length) {
  for (const message of errors) console.error(`::error::${message}`);
  process.exit(1);
}
console.log(
  `Source line check passed (${Object.keys(baseline).length} baselined files, limit ${lineLimit} lines).`,
);

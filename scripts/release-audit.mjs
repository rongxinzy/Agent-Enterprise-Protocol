import {readFile, stat} from "node:fs/promises";
import path from "node:path";
import {fileURLToPath} from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const manifest = await readJSON("release/foundation-readiness.json");
const release = manifest.release ?? {};
const completed = manifest.completedGates ?? [];
const remaining = manifest.remainingGates ?? [];

assert(release.implementationProfile === "m2", "implementation profile must be m2");
assert(release.protocolVersion === "1.0", "protocol version must be 1.0");
assert(release.stage === "release-candidate", "release stage must remain release-candidate");
assert(Number.isInteger(release.completionPercent), "completion percent must be an integer");

validateGates(completed, "completed");
validateGates(remaining, "remaining");
const completedWeight = sumWeights(completed);
const remainingWeight = sumWeights(remaining);
assert(completedWeight === release.completionPercent, "completed gate weights do not match completion percent");
assert(completedWeight + remainingWeight === 100, "completed and remaining gate weights must total 100");
assert(new Set([...completed, ...remaining].map(gate => gate.id)).size === completed.length + remaining.length, "gate IDs must be unique");

for (const gate of completed) {
  assert(Array.isArray(gate.evidence) && gate.evidence.length > 0, "completed gate " + gate.id + " requires evidence");
  for (const evidence of gate.evidence) {
    const evidencePath = resolveInsideRoot(evidence);
    const details = await stat(evidencePath).catch(() => null);
    assert(details?.isFile(), "missing evidence file: " + evidence);
  }
}
for (const gate of remaining) {
  assert(Array.isArray(gate.exitCriteria) && gate.exitCriteria.length > 0, "remaining gate " + gate.id + " requires exit criteria");
}

const packageDocument = await readJSON("package.json");
assert(packageDocument.version === release.packageVersion, "package version does not match release manifest");
assert(packageDocument.scripts?.["release:audit"] === "node scripts/release-audit.mjs", "release:audit script is not wired");
for (const command of ["npm run check", "npm run release:audit", "go test ./...", "go test -race ./...", "go vet ./...", "go build ./...", "npm run test:e2e"]) {
  assert(packageDocument.scripts?.["release:check"]?.includes(command), "release:check omits " + command);
}

const constants = await readText("packages/aep-sdk-node/src/constants.ts");
const protocolMatch = constants.match(/AEP_PROTOCOL_VERSION\s*=\s*'([^']+)'/);
assert(protocolMatch?.[1] === release.protocolVersion, "SDK protocol version does not match release manifest");

const goModule = await readText("go.mod");
assert(/^go\s+1\.26(?:\.\d+)?$/m.test(goModule), "Go 1.26 baseline is not pinned");

const controlConfig = await readText("services/control-service/internal/config/config.go");
assert(controlConfig.includes("AEP_ENABLE_MOCK_FEDERATED_AUTH must be false in production"), "production mock federated-auth guard is missing");
const controlServer = await readText("services/control-service/internal/httpapi/server.go");
assert(controlServer.includes("http.StatusUpgradeRequired"), "protocol-version 426 gate is missing");
assert(controlServer.includes("s.app.Config.EnableMockFederatedAuth"), "metadata does not conditionally advertise mock federated auth");

for (const sample of ["deploy/production/control-service.env.example", "deploy/production/gateway-authorizer.env.example"]) {
  const content = await readText(sample);
  assert(content.includes("AEP_ENVIRONMENT=production"), sample + " is not a production profile");
  for (const forbidden of ["change-this-admin-password", "minioadmin"]) {
    assert(!content.includes(forbidden), sample + " contains a forbidden development default");
  }
}
const productionControl = await readText("deploy/production/control-service.env.example");
assert(productionControl.includes("AEP_ENABLE_MOCK_FEDERATED_AUTH=false"), "production sample must explicitly disable mock federated auth");

const workflow = await readText(".github/workflows/m0.yml");
assert(workflow.includes("npm run release:audit"), "CI does not run the release audit");
assert(workflow.includes("go test -race ./..."), "CI does not run the Go race detector");
assert(workflow.includes("release-gate:"), "CI does not aggregate the release gate");

for (const readme of ["README.md", "README.zh-CN.md"]) {
  const content = await readText(readme);
  assert(content.includes("release-readiness"), readme + " does not link the release-readiness report");
}

assert(!manifest.productionCapabilities.includes("federated_auth"), "mock federated auth cannot be a production capability");
assert(manifest.developmentOnlyCapabilities.includes("federated_auth"), "mock federated auth must be marked development-only");

console.log("AEP release audit passed: " + completedWeight + "% complete, " + remainingWeight + "% explicitly gated.");

function validateGates(gates, label) {
  assert(Array.isArray(gates) && gates.length > 0, label + " gates must be a non-empty array");
  for (const gate of gates) {
    assert(typeof gate.id === "string" && gate.id.length > 0, label + " gate ID is required");
    assert(Number.isInteger(gate.weight) && gate.weight > 0, label + " gate " + gate.id + " has an invalid weight");
  }
}

function sumWeights(gates) {
  return gates.reduce((total, gate) => total + gate.weight, 0);
}

function resolveInsideRoot(relativePath) {
  assert(typeof relativePath === "string" && relativePath.length > 0, "evidence path is required");
  const resolved = path.resolve(root, relativePath);
  assert(resolved === root || resolved.startsWith(root + path.sep), "path escapes repository root: " + relativePath);
  return resolved;
}

async function readJSON(relativePath) {
  return JSON.parse(await readText(relativePath));
}

async function readText(relativePath) {
  return readFile(resolveInsideRoot(relativePath), "utf8");
}

function assert(condition, message) {
  if (!condition) throw new Error("release audit failed: " + message);
}
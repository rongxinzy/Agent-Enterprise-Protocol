import { spawnSync, spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import { writeFile } from "node:fs/promises";
import path from "node:path";
import assert from "node:assert/strict";
// Explicitly destructive local recovery acceptance; never use a customer cluster.
// Credentials are read into memory from the existing disposable fixture Secret.
assert.equal(
  process.env.AEP_RECOVERY_ACCEPT_LOCAL_RESET,
  "true",
  "Explicit local reset opt-in required",
);
const kubeconfig = process.env.AEP_RECOVERY_KUBECONFIG,
  context = process.env.AEP_RECOVERY_CONTEXT,
  cliPath = process.env.AEP_RECOVERY_CLI;
assert(
  kubeconfig && path.isAbsolute(kubeconfig),
  "Absolute dedicated kubeconfig required",
);
assert(
  /^kind-aep-operator-[a-z0-9-]+$/.test(context ?? ""),
  "Dedicated operator Kind context required",
);
assert(
  cliPath && path.isAbsolute(cliPath),
  "Absolute built aepctl path required",
);
const kube = ["--kubeconfig", kubeconfig, "--context", context];
function run(exe, args, { input, encoding = "utf8", env } = {}) {
  const r = spawnSync(exe, args, {
    encoding,
    input,
    env,
    windowsHide: true,
    maxBuffer: 150e6,
  });
  assert.equal(r.status, 0, exe + " local operation failed");
  return r.stdout;
}
const k = (args, input) => run("kubectl", [...kube, ...args], { input });
const get = (kind, name, ns = "aep-system") =>
  JSON.parse(k(["-n", ns, "get", kind, ...(name ? [name] : []), "-o", "json"]));
const patch = (kind, name, value, ns = "aep-system") =>
  k([
    "-n",
    ns,
    "patch",
    kind,
    name,
    "--type=merge",
    "--patch",
    JSON.stringify(value),
  ]);
const env = Object.fromEntries(
  Object.entries(get("secret", "operator-local-control-env").data).map(
    ([n, v]) => [n, Buffer.from(v, "base64").toString()],
  ),
);
assert(
  ["development", "test"].includes(env.AEP_ENVIRONMENT),
  "Disposable development/test fixture required",
);
assert(
  get("modelroutes", "").items.every((r) => !r.spec.enabled),
  "All original routes must be disabled",
);
assert(
  get("deployments", "").items.every(
    (d) => !d.metadata.name.includes("reconciler"),
  ),
  "Legacy writer present",
);
const original = get("deployment", "control-service"),
  publisher = get("deployment", "aep-credential-publisher");
let forward,
  token,
  dbCreated = false,
  secretCreated = false,
  changed = false;
const db = "aep_recovery_" + Date.now(),
  secret = "operator-recovery-db-" + Date.now();
const report = { startedAt: new Date().toISOString(), checks: [] };
const wait = (ms) => new Promise((r) => setTimeout(r, ms));
async function until(fn, timeout = 90000) {
  let last;
  for (const end = Date.now() + timeout; Date.now() < end; ) {
    try {
      const v = await fn();
      if (v) return v;
    } catch (e) {
      last = e;
    }
    await wait(1000);
  }
  throw last ?? Error("convergence timeout");
}
async function pf() {
  if (forward) {
    forward.kill();
    await wait(400);
  }
  forward = spawn(
    "kubectl",
    [
      ...kube,
      "-n",
      "aep-system",
      "port-forward",
      "--address",
      "127.0.0.1",
      "svc/control-service",
      "28284:8080",
    ],
    { windowsHide: true, stdio: "ignore" },
  );
  await until(async () => {
    const r = await fetch("http://127.0.0.1:28284/livez", {
      signal: AbortSignal.timeout(2000),
    });
    return r.ok;
  }, 15000);
}
async function api(path, method = "GET", body, headers = {}) {
  const r = await fetch("http://127.0.0.1:28284/aep/v1/" + path, {
    method,
    headers: {
      "X-AEP-Protocol-Version": "1.0",
      "Content-Type": "application/json",
      ...(token ? { Authorization: "Bearer " + token } : {}),
      ...headers,
    },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(10000),
  });
  return { status: r.status, body: r.status === 204 ? null : await r.json() };
}
async function login() {
  const r = await api("auth/password/login", "POST", {
    username: env.AEP_BOOTSTRAP_ADMIN_USERNAME,
    password: env.AEP_BOOTSTRAP_ADMIN_PASSWORD,
  });
  assert.equal(r.status, 200);
  token = r.body.accessToken;
}
const cliEnv = {
  ...process.env,
  AEPCTL_BASE_URL: "http://127.0.0.1:28284",
  AEPCTL_DEPLOYMENT: "demo",
  AEPCTL_USERNAME: env.AEP_BOOTSTRAP_ADMIN_USERNAME,
  AEPCTL_PASSWORD: env.AEP_BOOTSTRAP_ADMIN_PASSWORD,
};
const cli = (...args) =>
  JSON.parse(
    run(cliPath, ["data-plane", "publication", ...args], { env: cliEnv }),
  );
async function ready(id) {
  return until(async () => {
    const r = await api("admin/data-plane/publications/" + id);
    assert.equal(r.status, 200);
    return r.body.conditions?.find(
      (c) =>
        c.type === "Ready" &&
        c.status === "True" &&
        c.observedRevision === r.body.revision,
    )
      ? r.body
      : false;
  });
}
function scale(name, n) {
  k(["-n", "aep-system", "scale", "deployment/" + name, "--replicas=" + n]);
  if (n === 0) {
    const label =
      name === "control-service"
        ? "app=control-service"
        : name === "aep-gateway-operator"
          ? "app=aep-gateway-operator"
          : "app.kubernetes.io/name=aep-credential-publisher";
    k([
      "-n",
      "aep-system",
      "wait",
      "--for=delete",
      "pods",
      "-l",
      label,
      "--timeout=120s",
    ]);
  } else
    k([
      "-n",
      "aep-system",
      "rollout",
      "status",
      "deployment/" + name,
      "--timeout=120s",
    ]);
}
const pg = get("pods", "", "aep-infra").items.find((p) =>
  p.metadata.name.startsWith("postgres-"),
).metadata.name;
const pgEnv = Object.fromEntries(
  get("pod", pg, "aep-infra").spec.containers[0].env.map((e) => [
    e.name,
    e.value,
  ]),
);
const pgUser = pgEnv.POSTGRES_USER,
  pgDB = pgEnv.POSTGRES_DB;
assert(pgUser && pgDB);
const psql = (sql) =>
  k(
    [
      "-n",
      "aep-infra",
      "exec",
      "-i",
      pg,
      "--",
      "psql",
      "-X",
      "-v",
      "ON_ERROR_STOP=1",
      "-U",
      pgUser,
      "-d",
      pgDB,
    ],
    sql,
  );
try {
  await pf();
  await login();
  const key = "cli-queue-" + randomUUID(),
    queued = cli("queue", "--key", key, "--include-gateway-limits");
  assert(queued.operationId);
  const same = cli("queue", "--key", key, "--include-gateway-limits");
  assert.equal(same.operationId, queued.operationId);
  await ready(queued.operationId);
  const recoveryKey = "cli-replay-" + randomUUID(),
    replayed = cli(
      "replay",
      "--operation-id",
      queued.operationId,
      "--key",
      recoveryKey,
    );
  assert.notEqual(replayed.operationId, queued.operationId);
  assert.equal(replayed.contentHash, queued.contentHash);
  assert.equal(
    cli("replay", "--operation-id", queued.operationId, "--key", recoveryKey)
      .operationId,
    replayed.operationId,
  );
  await ready(replayed.operationId);
  const observed = cli("get", "--operation-id", replayed.operationId);
  assert.equal(observed.operationId, replayed.operationId);
  const stale = await api(
    "admin/data-plane/publications/" + queued.operationId + "/replay",
    "POST",
    {},
    { "Idempotency-Key": "stale-" + randomUUID() },
  );
  assert.equal(stale.status, 409);
  assert.equal(stale.body.code, "PUBLICATION_NOT_LATEST");
  report.checks.push(
    "Real CLI queue/get/replay; duplicate keys return same operation; replay preserves immutable hash; stale source rejected409",
  );
  // Take a consistent PostgreSQL backup at T, restore into a separate temporary DB.
  const snapshot = run(
    "kubectl",
    [
      ...kube,
      "-n",
      "aep-infra",
      "exec",
      pg,
      "--",
      "pg_dump",
      "-U",
      pgUser,
      "-d",
      pgDB,
      "--format=custom",
      "--no-owner",
      "--no-acl",
    ],
    { encoding: null },
  );
  psql("CREATE DATABASE " + db + ";");
  dbCreated = true;
  run(
    "kubectl",
    [
      ...kube,
      "-n",
      "aep-infra",
      "exec",
      "-i",
      pg,
      "--",
      "pg_restore",
      "-U",
      pgUser,
      "-d",
      db,
      "--no-owner",
      "--no-acl",
    ],
    { input: snapshot, encoding: null },
  );
  snapshot.fill(0);
  const newer = cli(
    "replay",
    "--operation-id",
    replayed.operationId,
    "--key",
    "newer-" + randomUUID(),
  );
  await ready(newer.operationId);
  const ahead = get("deploymentconfigs", "deployment");
  const marks = Object.fromEntries(
    Object.entries(ahead.metadata.annotations ?? {}).filter(([key]) =>
      key.startsWith("gateway.aep.zhiyuan.io/"),
    ),
  );
  changed = true;
  scale("aep-credential-publisher", 0);
  scale("control-service", 0);
  const url = new URL(env.AEP_DATABASE_URL);
  url.pathname = "/" + db;
  k(
    ["create", "-f", "-"],
    JSON.stringify({
      apiVersion: "v1",
      kind: "Secret",
      metadata: { name: secret, namespace: "aep-system" },
      stringData: { url: url.href },
    }),
  );
  secretCreated = true;
  const containers = structuredClone(original.spec.template.spec.containers);
  containers[0].env = [
    ...(containers[0].env ?? []).filter((e) => e.name !== "AEP_DATABASE_URL"),
    {
      name: "AEP_DATABASE_URL",
      valueFrom: { secretKeyRef: { name: secret, key: "url" } },
    },
  ];
  patch("deployment", "control-service", {
    spec: { template: { spec: { containers } } },
  });
  scale("control-service", original.spec.replicas);
  await pf();
  await login();
  const blocked = cli(
    "replay",
    "--operation-id",
    replayed.operationId,
    "--key",
    "restored-" + randomUUID(),
  );
  await until(async () => {
    const p = await api("admin/data-plane/publications/" + blocked.operationId);
    return ["superseded", "failed"].includes(p.body.phase);
  });
  const after = get("deploymentconfigs", "deployment");
  for (const [key, v] of Object.entries(marks))
    assert.equal(
      after.metadata.annotations[key],
      v,
      "older DB overwrote newer CR",
    );
  report.checks.push(
    "Actual pg_dump/pg_restore to isolated DB T while live CR remains T+1: relay rejects old sequence and does not mutate newer CR",
  );
  // Recover the coherent original DB T+1. No clearing markers or forced takeover.
  scale("control-service", 0);
  patch("deployment", "control-service", {
    spec: { template: original.spec.template },
  });
  scale("control-service", original.spec.replicas);
  scale("aep-credential-publisher", publisher.spec.replicas);
  await pf();
  await login();
  const recovered = cli(
    "replay",
    "--operation-id",
    newer.operationId,
    "--key",
    "coherent-" + randomUUID(),
  );
  await ready(recovered.operationId);
  report.checks.push(
    "Joint recovery restores coherent original DB T+1; replay reaches fresh matching full-fleet Ready without weakening sequence or ownership guards",
  );
  // A deleted disabled route can be rebuilt by replay with a new UID.
  const routes = get("modelroutes", "").items;
  assert(
    routes.every((r) => !r.spec.enabled),
    "recovery fixture requires all original routes disabled",
  );
  const route = routes[0];
  assert(route);
  k([
    "-n",
    "aep-system",
    "delete",
    "modelroute",
    route.metadata.name,
    "--wait=false",
  ]);
  await until(() => {
    const owned = get(
      "ingresses,envoyfilters,wasmplugins",
      "",
      "higress-system",
    ).items.filter(
      (o) =>
        o.metadata.labels?.["gateway.aep.zhiyuan.io/owner-uid"] ===
        route.metadata.uid,
    );
    return owned.length === 0;
  });
  // Managed publication membership is immutable: deleting one CR deliberately
  // holds the finalizer and closes routing. This is a reviewed recovery fixture,
  // so discharge its verified-empty cleanup duties before simulating CR loss.
  const terminating = get("modelroute", route.metadata.name);
  assert(terminating.metadata.deletionTimestamp);
  const index = terminating.metadata.finalizers.indexOf(
    "gateway.aep.zhiyuan.io/rendered-resources",
  );
  assert(index >= 0);
  k([
    "-n",
    "aep-system",
    "patch",
    "modelroute",
    route.metadata.name,
    "--type=json",
    "--patch",
    JSON.stringify([
      { op: "test", path: "/metadata/uid", value: terminating.metadata.uid },
      {
        op: "test",
        path: "/metadata/resourceVersion",
        value: terminating.metadata.resourceVersion,
      },
      { op: "remove", path: "/metadata/finalizers/" + index },
    ]),
  ]);
  const rebuilt = cli(
    "replay",
    "--operation-id",
    recovered.operationId,
    "--key",
    "rebuild-" + randomUUID(),
  );
  await ready(rebuilt.operationId);
  assert.notEqual(
    get("modelroute", route.metadata.name).metadata.uid,
    route.metadata.uid,
  );
  k([
    "-n",
    "aep-system",
    "rollout",
    "restart",
    "deployment/aep-gateway-operator",
  ]);
  k([
    "-n",
    "aep-system",
    "rollout",
    "status",
    "deployment/aep-gateway-operator",
    "--timeout=120s",
  ]);
  await ready(rebuilt.operationId);
  report.checks.push(
    "Direct CR deletion fails closed with finalizer; after verified-empty owned cleanup, simulated CR loss rebuilt by immutable replay/new UID; two-replica restart returns Ready",
  );
  // Pause relay but keep mode true: old writer paths must stay shut.
  scale("control-service", 0);
  const paused = structuredClone(original.spec.template);
  paused.spec.containers[0].env = [
    ...(paused.spec.containers[0].env ?? []).filter(
      (e) => e.name !== "AEP_GATEWAY_PUBLICATION_RELAY_ENABLED",
    ),
    { name: "AEP_GATEWAY_PUBLICATION_RELAY_ENABLED", value: "false" },
  ];
  patch("deployment", "control-service", { spec: { template: paused } });
  scale("control-service", original.spec.replicas);
  await pf();
  await login();
  const denied = await api("admin/data-plane/publish", "POST", {});
  assert.equal(denied.status, 503);
  assert.equal(denied.body.code, "LEGACY_GATEWAY_PUBLICATION_DISABLED");
  const status = await api("admin/data-plane/status");
  assert.equal(status.status, 200);
  assert.equal(status.body.state, "ready");
  report.checks.push(
    "Relay paused with explicit mode: legacy publish stays503 and read-only matching observation staysavailable",
  );
  report.status = "passed";
} catch (e) {
  report.status = "failed";
  report.error = e.message;
  process.exitCode = 1;
} finally {
  try {
    if (changed) {
      scale("control-service", 0);
      patch("deployment", "control-service", {
        spec: { template: original.spec.template },
      });
      scale("control-service", original.spec.replicas);
      scale("aep-credential-publisher", publisher.spec.replicas);
    }
    if (secretCreated) k(["-n", "aep-system", "delete", "secret", secret]);
    if (dbCreated) psql("DROP DATABASE " + db + " WITH (FORCE);");
    report.cleanup =
      "Original DB/template/publisher restored; temporary DB and Secret removed; no force takeover or production changes";
  } catch (e) {
    report.cleanupError = e.message;
    process.exitCode = 1;
  }
  if (forward) forward.kill();
  report.completedAt = new Date().toISOString();
  if (process.env.AEP_RECOVERY_REPORT)
    await writeFile(
      process.env.AEP_RECOVERY_REPORT,
      JSON.stringify(report, null, 2),
    );
  console.log(JSON.stringify(report));
}

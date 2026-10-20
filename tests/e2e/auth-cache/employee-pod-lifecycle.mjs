// Explicit safety probe: a deleted bound Pod must not retain a warm identity.
// Tokens remain in memory/stdin and never enter logs or the report.
import assert from 'node:assert/strict';

export async function employeePodLifecycle({kubectl, until, get, record, report, fleet, runtime, alice, bob, model}) {
  const credential = alice.tokens.modelAccessToken;
  const claims = JSON.parse(Buffer.from(credential.split('.')[1], 'base64url'));
  const bound = claims['kubernetes.io'].pod;
  assert(bound?.name && bound?.uid);
  assert(claims.exp > Date.now() / 1000 + 180, 'Probe needs a live token beyond the Kubernetes deletion grace');
  const review = async () => JSON.parse(await kubectl(['create', '-f', '-', '-o', 'json'], {input: JSON.stringify({
    apiVersion: 'authentication.k8s.io/v1', kind: 'TokenReview',
    spec: {token: credential, audiences: [claims.aud[0]]},
  })}));
  assert.equal((await review()).status.authenticated, true, 'Live bound-Pod control must authenticate');
  const before = await get('authconfig', 'access-' + alice.binding);
  await fleet.check(alice, model.id, 200);
  await fleet.check(bob, model.id, 200);
  await kubectl(['-n', 'digital-employees', 'delete', 'pod', bound.name, '--wait=true', '--timeout=120s']);
  await kubectl(['-n', 'digital-employees', 'rollout', 'status', 'deployment/de-' + alice.name, '--timeout=180s']);
  const current = JSON.parse(await kubectl(['-n', 'digital-employees', 'get', 'pods', '-o', 'json'])).items;
  assert(!current.some(p => p.metadata.uid === bound.uid), 'Old bound Pod still exists');
  const started = Date.now();
  await until(async () => {
    const result = await review();
    assert(result.status, 'TokenReview must return a completed status');
    // Native false is optional/omitted in Kubernetes JSON responses.
    assert.equal(result.status.authenticated ?? false, false, 'API Server still considers the deleted-Pod token valid');
  }, 120_000);
  assert(claims.exp > Date.now() / 1000, 'Token expiry must not satisfy lifecycle revocation');
  assert.equal((await runtime.modelCall(alice.name)).status, 200, 'Replacement Pod must retain legitimate model access');
  await fleet.check(bob, model.id, 200);
  const peerUnchanged = await get('authconfig', 'access-' + bob.binding);
  let warmAccepted = false;
  try { await fleet.check(alice, model.id, 200); warmAccepted = true; }
  catch { await fleet.check(alice, model.id, 403); }
  const after = await get('authconfig', 'access-' + alice.binding);
  report.employeePodLifecycle = {apiServerRejectsDeletedPodToken: true, tokenStillUnexpired: true,
    replacementPodAllowed: true, peerAllowed: true, warmDeletedPodTokenAccepted: warmAccepted,
    authConfigUnchanged: before.metadata.resourceVersion === after.metadata.resourceVersion,
    apiRejectionWaitMs: Date.now() - started, peerConfigVersion: peerUnchanged.metadata.resourceVersion};
  assert.equal(warmAccepted, false, 'Deleted-Pod token remains accepted by the warm verifier fleet after native TokenReview rejects it');
  record('Deleted Pod credential is denied while the replacement Pod and unrelated employee remain allowed');
}

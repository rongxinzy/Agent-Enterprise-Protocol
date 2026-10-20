// Explicit safety probe: a deleted bound Pod must not retain a warm identity.
// Tokens remain in memory/stdin and never enter logs or the report.
import assert from 'node:assert/strict';

export async function employeePodLifecycle({kubectl, until, get, record, report, fleet, runtime, alice, bob, model, reviews}) {
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
  const grantName = 'access-' + alice.binding;
  await until(async () => assert((await get('modelaccessgrant', grantName)).spec.workloadPodUids?.includes(bound.uid),
    'Trusted observer has not published the original live Pod'));
  const before = await get('authconfig', 'access-' + alice.binding);
  const peerBefore = await get('authconfig', 'access-' + bob.binding);
  await fleet.check(alice, model.id, 200);
  await fleet.check(bob, model.id, 200);
  const deletedAt = Date.now();
  await kubectl(['-n', 'digital-employees', 'delete', 'pod', bound.name, '--wait=true', '--timeout=120s']);
  await kubectl(['-n', 'digital-employees', 'rollout', 'status', 'deployment/de-' + alice.name, '--timeout=180s']);
  const current = JSON.parse(await kubectl(['-n', 'digital-employees', 'get', 'pods', '-o', 'json'])).items;
  assert(!current.some(p => p.metadata.uid === bound.uid), 'Old bound Pod still exists');
  const replacement = current.find(p => !p.metadata.deletionTimestamp &&
    p.spec.serviceAccountName === claims['kubernetes.io'].serviceaccount.name);
  assert(replacement?.metadata.uid, 'Replacement employee Pod is missing');
  await until(async () => {
    const grant = await get('modelaccessgrant', grantName);
    assert(!grant.spec.disabled && grant.spec.workloadPodUids?.includes(replacement.metadata.uid) &&
      !grant.spec.workloadPodUids.includes(bound.uid), 'Trusted publication has not adopted replacement Pod membership');
  });
  const publicationWaitMs = Date.now() - deletedAt;
  const started = Date.now();
  await until(async () => {
    const result = await review();
    assert(result.status, 'TokenReview must return a completed status');
    // Native false is optional/omitted in Kubernetes JSON responses.
    assert.equal(result.status.authenticated ?? false, false, 'API Server still considers the deleted-Pod token valid');
  }, 120_000);
  const apiRejectionWaitMs = Date.now() - started;
  await until(() => fleet.check(alice, model.id, 403), 120_000);
  const revocationWaitMs = Date.now() - deletedAt;
  assert(claims.exp > Date.now() / 1000, 'Token expiry must not satisfy lifecycle revocation');
  assert.equal((await runtime.modelCall(alice.name)).status, 200, 'Replacement Pod must retain legitimate model access');
  await fleet.check(bob, model.id, 200);
  const peerUnchanged = await get('authconfig', 'access-' + bob.binding);
  // All explicit native TokenReviews have finished. Repeated old-bearer denial
  // must now reuse authentication; the current Pod policy does the rejecting.
  const reviewsBeforeWarmDenial = await reviews();
  for (let i = 0; i < 3; i++) await fleet.check(alice, model.id, 403);
  const reviewsAfterWarmDenial = await reviews();
  assert.equal(reviewsAfterWarmDenial, reviewsBeforeWarmDenial, 'Pod revocation repeated native authentication');
  const after = await get('authconfig', 'access-' + alice.binding);
  report.employeePodLifecycle = {apiServerRejectsDeletedPodToken: true, tokenStillUnexpired: true,
    replacementPodAllowed: true, peerAllowed: true, warmDeletedPodTokenAccepted: false,
    authConfigUnchanged: before.metadata.resourceVersion === after.metadata.resourceVersion,
    publicationWaitMs, apiRejectionWaitMs, revocationWaitMs, reviewsBeforeWarmDenial, reviewsAfterWarmDenial,
    verifierProcesses: fleet.identities()};
  assert.equal(after.metadata.resourceVersion, before.metadata.resourceVersion, 'Pod churn rebuilt employee authentication caches');
  assert.equal(peerUnchanged.metadata.resourceVersion, peerBefore.metadata.resourceVersion, 'Pod churn changed peer policy');
  record('Deleted Pod credential is denied while the replacement Pod and unrelated employee remain allowed');
}

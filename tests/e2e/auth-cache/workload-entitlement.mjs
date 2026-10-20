// Native verifier/Operator license checks. Explicit CR fixtures are not proof
// of verified-license DB capture, relay delivery or employee product adoption.
import assert from 'node:assert/strict';

export async function workloadEntitlementChecks({a, b, ports, call, peer, apply, until, get, record, reviewCount}) {
  const peerVersion = (await get('authconfig', 'access-' + b.binding)).metadata.resourceVersion;
  const selected = a.spec.licenses[0];
  const deadline = Math.floor(Date.now() / 1000) + 90;
  selected.validUntil = new Date(deadline * 1000).toISOString();
  a.spec.licenses.push({id: 'another-perpetual-license', digest: 'sha256:' + 'b'.repeat(64)});
  a.spec.publication.sequence++; a.spec.publication.revision = 'selected-license-grace';
  await apply([a.grant()]);
  for (const port of ports) await until(async () => {
    const result = await call(port);
    assert.equal(result.status, 200);
    assert.equal(result.observation.publication.sequence, a.spec.publication.sequence);
  });
  const beforePeer = await reviewCount();
  for (const port of ports) assert.equal((await call(port, b, peer)).status, 200);
  assert.equal(await reviewCount(), beforePeer, 'Changing this license must not invalidate the warm peer identity');
  const reviews = await reviewCount();
  await until(async () => assert(Date.now() / 1000 > deadline + 0.2));
  for (const port of ports) {
    assert.equal((await call(port)).status, 403);
    assert.equal((await call(port, b, peer)).status, 200);
  }
  assert.equal(await reviewCount(), reviews, 'Warm license expiry must deny without another TokenReview');
  record('Exact workload license grace expires on both warm replicas; another active license cannot substitute and peer stays allowed', {peerCachePreserved: true});

  // Replacement requires an explicit newer trusted publication, not fallback
  // selection of whichever active license happens to remain in the list.
  a.spec.workloadEntitlement = {licenseId: a.spec.licenses[1].id, licenseDigest: a.spec.licenses[1].digest, feature: 'enterprise.models'};
  a.spec.publication.sequence++; a.spec.publication.revision = 'explicit-license-replacement';
  await apply([a.grant()]);
  for (const port of ports) await until(async () => assert.equal((await call(port)).status, 200));
  record('Explicit newer exact-license replacement restores workload access');

  const licenses = a.spec.licenses, entitlement = a.spec.workloadEntitlement;
  a.spec.licenses = []; delete a.spec.workloadEntitlement;
  a.spec.disabled = true; a.spec.models = [];
  a.spec.publication.sequence++; a.spec.publication.revision = 'workload-license-revoked';
  await apply([a.grant()]);
  for (const port of ports) await until(async () => assert.equal((await call(port)).status, 403));
  const beforeRevokedPeer = await reviewCount();
  for (const port of ports) assert.equal((await call(port, b, peer)).status, 200);
  assert.equal(await reviewCount(), beforeRevokedPeer, 'Revoking this license must not invalidate the warm peer identity');
  assert.equal((await get('authconfig', 'access-' + b.binding)).metadata.resourceVersion, peerVersion);
  record('License removal applies a disabled tombstone even with entitlement required; unrelated employee configuration stays unchanged', {peerCachePreserved: true});

  a.spec.licenses = licenses; a.spec.workloadEntitlement = entitlement;
  a.spec.disabled = false; a.spec.models = ['model-a'];
  a.spec.publication.sequence++; a.spec.publication.revision = 'licensed-workload-restored';
  await apply([a.grant()]);
  for (const port of ports) await until(async () => assert.equal((await call(port)).status, 200));
}

// Native API-server metrics witness identity-cache reuse, not decision-cache reuse.
import assert from 'node:assert/strict';

export async function employeeCacheWitness({kubectl, fleet, alice, bob, model}) {
  const reviews = async () => (await kubectl(['get', '--raw', '/metrics'])).split('\n')
    .filter(line => line.startsWith('apiserver_request_total{') && line.includes('resource="tokenreviews"') &&
      /verb="(?:CREATE|POST)"/.test(line))
    .reduce((total, line) => total + Number(line.slice(line.lastIndexOf(' ') + 1)), 0);
  const coldReviews = await reviews();
  assert(coldReviews >= 4, 'Both employee credentials must have been reviewed on both serving processes');
  for (let i = 0; i < 3; i++) {
    await fleet.check(alice, model.id, 200); await fleet.check(bob, model.id, 200);
  }
  assert.equal(await reviews(), coldReviews, 'Warm employee calls repeated TokenReview');
  return {reviews, coldReviews};
}

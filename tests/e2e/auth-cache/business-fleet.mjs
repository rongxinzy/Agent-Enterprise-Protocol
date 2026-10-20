// Acceptance observer only: enumerate the actual current serving Pod/process set.
// It never writes product Ready/status or claims a production observer exists.
import assert from 'node:assert/strict';

export async function businessFleet({kubectl, forward, request}) {
  const discover = async () => {
    const pods = JSON.parse(await kubectl(['-n', 'aep-system', 'get', 'pods', '-l', 'aep-acceptance-authorino=true', '-o', 'json'])).items;
    assert.equal(pods.filter(p => !p.metadata.deletionTimestamp).length, 2, 'Incomplete or changing serving fleet');
    return pods.filter(p => !p.metadata.deletionTimestamp).map(pod => {
      assert.equal(pod.status.conditions.find(c => c.type === 'Ready')?.status, 'True');
      assert.equal(pod.status.containerStatuses.length, 1);
      const process = pod.status.containerStatuses[0];
      assert(process.ready && process.containerID);
      return {name: pod.metadata.name, uid: pod.metadata.uid, instance: process.containerID, restarts: process.restartCount};
    }).sort((a, b) => a.uid.localeCompare(b.uid));
  };
  let captured;
  let ports = [];
  async function refresh() {
    captured = await discover();
    ports = await Promise.all(captured.map(p => forward('pod/' + p.name, 5001)));
  }
  await refresh();
  return {
    discover, refresh,
    identities: () => captured.map(({uid, instance, restarts}) => ({uid, instance, restarts})),
    async check(person, model, status, publication) {
      assert.deepEqual(await discover(), captured, 'Fleet changed: rediscover all processes instead of trusting old endpoints');
      const observations = [];
      for (let index = 0; index < ports.length; index++) {
        const result = await request(ports[index], person.binding + '.access.aep.invalid', person.tokens.modelAccessToken, model);
        assert.equal(result.status, status, 'Native process did not enforce the expected business policy');
        if (publication && status === 200) assert.deepEqual(result.observation.publication, publication);
        observations.push({podUid: captured[index].uid, instanceId: captured[index].instance, status,
          ...(status === 200 ? {semanticHash: result.observation.semanticHash, publication: result.observation.publication} : {})});
      }
      assert.deepEqual(await discover(), captured, 'Fleet changed while collecting process observations');
      return observations;
    },
  };
}

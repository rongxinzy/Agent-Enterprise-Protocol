// Real PostgreSQL + HTTP acceptance for the queue-only stage. This does not
// exercise CR projection or Ready detection; all conditions must stay Unknown.
export async function verifyDataPlanePublications({admin, model, sql, restart}) {
  const publications = await Promise.all(Array.from({length: 12}, () =>
    admin.createDataPlanePublication('concurrent-publication', {revision: 'async-initial'})));
  const first = publications[0];
  assert(publications.every(item => item.operationId === first.operationId), 'concurrent retries created more than one operation');
  assert(first.phase === 'queued', 'publication must only be queued');
  assert(Date.parse(first.idempotencyExpiresAt) - Date.parse(first.createdAt) >= 86_400_000, 'idempotency window is shorter than 24h');
  assert(await sql('SELECT count(*) FROM data_plane_publications') === '1', 'operation was duplicated');
  assert(await sql('SELECT count(*) FROM data_plane_publication_outbox') === '1', 'outbox was duplicated');
  assert(await sql("SELECT count(*) FROM admin_audit_events WHERE resource_type='data_plane_publication'") === '1', 'audit was duplicated');
  assert(await sql("SELECT count(*) FROM data_plane_desired_states") === '0', 'queue-only path changed the legacy desired state');
  const safeSnapshot = await sql('SELECT payload::text FROM data_plane_publication_snapshots');
  assert(!safeSnapshot.includes('provider-secret-value-a') && safeSnapshot.includes(`aep-credential-${model.credentialId}`), 'snapshot must contain only credential references');
  assert(!JSON.stringify(first).includes('credentialRef') && !JSON.stringify(first).includes('idempotencyKeyHash'), 'publication response exposed internal state');
  assert(await sql("SELECT count(*) FROM data_plane_publications WHERE idempotency_key_hash='concurrent-publication'") === '0', 'raw idempotency key was stored');

  // First publication's snapshot stays frozen even after catalog edits.
  await admin.updateModel(model.id, {upstreamModel: 'publication-next'});
  const replay = await admin.createDataPlanePublication('concurrent-publication', {revision: ' async-initial '});
  assert(replay.operationId === first.operationId && replay.contentHash === first.contentHash, 'replay rederived a changed catalog');
  await expectProblem(admin.createDataPlanePublication('concurrent-publication', {revision: 'different'}), 409, 'IDEMPOTENCY_KEY_CONFLICT');
  await expectProblem(admin.createDataPlanePublication('revision-collision', {revision: 'async-initial'}), 422, 'INVALID_DATA_PLANE_STATE');

  // Distinct intents serialize independently; equal snapshots may be reused,
  // but operations and outbox entries must not be dropped or share a sequence.
  const distinct = await Promise.all(Array.from({length: 6}, (_, index) => admin.createDataPlanePublication(`distinct-${index}`)));
  assert(new Set(distinct.map(item => item.operationId)).size === 6, 'independent keys shared an operation');
  assert(await sql('SELECT count(*) = count(DISTINCT sequence) FROM data_plane_publications') === 't', 'publication order was not unique');

  // Storage is deployment-scoped even when keys/revisions are identical.
  // This is isolated fixture SQL, not an application API or a real deployment.
  await sql(`INSERT INTO deployments(id,name) VALUES ('publication-other','Other');
    INSERT INTO data_plane_publication_snapshots(deployment_id,revision,content_hash,payload)
      SELECT 'publication-other',revision,content_hash,payload FROM data_plane_publication_snapshots WHERE revision='async-initial';
    INSERT INTO data_plane_publications(deployment_id,operation_id,idempotency_key_hash,request_hash,revision,content_hash)
      SELECT 'publication-other','21347ddd-5234-4b3d-8936-a45f683e5f56',idempotency_key_hash,request_hash,revision,content_hash
      FROM data_plane_publications WHERE operation_id='${first.operationId}';`);
  await expectProblem(admin.getDataPlanePublication('21347ddd-5234-4b3d-8936-a45f683e5f56'), 404, 'RESOURCE_NOT_FOUND');

  // Fail at the last two writes and prove that the earlier snapshot and
  // operation (and in the outbox case audit) do not survive rollback.
  for (const [table, revision] of [['admin_audit_events', 'rollback-audit'], ['data_plane_publication_outbox', 'rollback-outbox']]) {
    const audit = table === 'admin_audit_events';
    await sql(`CREATE FUNCTION publication_fixture_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
      ${audit ? "IF NEW.resource_type <> 'data_plane_publication' THEN RETURN NEW; END IF;" : ''}
      RAISE EXCEPTION 'publication fixture rejected insert'; END $$;
      CREATE TRIGGER publication_fixture_fail BEFORE INSERT ON ${table} FOR EACH ROW EXECUTE FUNCTION publication_fixture_fail();`);
    try {
      await expectProblem(admin.createDataPlanePublication(revision, {revision}), 503, 'PUBLICATION_UNAVAILABLE');
      assert(await sql(`SELECT count(*) FROM data_plane_publication_snapshots WHERE revision='${revision}'`) === '0', 'failed transaction left a snapshot');
      assert(await sql(`SELECT count(*) FROM data_plane_publications WHERE revision='${revision}'`) === '0', 'failed transaction left an operation');
      assert(await sql(`SELECT count(*) FROM admin_audit_events WHERE resource_type='data_plane_publication' AND payload->'after'->>'revision'='${revision}'`) === '0', 'failed transaction left a success audit');
      assert(await sql('SELECT count(*) FROM data_plane_publication_outbox o LEFT JOIN data_plane_publications p USING (deployment_id,operation_id) WHERE p.operation_id IS NULL') === '0', 'orphan outbox intent');
    } finally {
      await sql(`DROP TRIGGER publication_fixture_fail ON ${table}; DROP FUNCTION publication_fixture_fail();`);
    }
    assert((await admin.createDataPlanePublication(revision, {revision})).phase === 'queued', 'original key was poisoned by a rolled-back transaction');
  }

  await restart();
  const restored = await admin.getDataPlanePublication(first.operationId);
  assert(restored.operationId === first.operationId && restored.contentHash === first.contentHash, 'restart lost the durable operation');
  assert(restored.conditions.length === 3 && restored.conditions.every(condition => condition.status === 'Unknown' && condition.observedRevision === null), 'queue-only operation fabricated an observation');
  assert((await admin.createDataPlanePublication('concurrent-publication', {revision: 'async-initial'})).operationId === first.operationId, 'restart lost the idempotency binding');
  assert(await sql(`SELECT count(*) FROM admin_audit_events WHERE resource_type='data_plane_publication' AND resource_id='${first.operationId}'`) === '1', 'replay appended another success audit');
  await admin.updateModel(model.id, {upstreamModel: model.upstreamModel});
  console.log('Publication queue acceptance passed: concurrency, immutable replay, tenant isolation, atomic rollback and restart.');
}

async function expectProblem(promise, status, code) {
  try { await promise; } catch (error) {
    assert(error.status === status && error.code === code, `expected ${status} ${code}, got ${error.status} ${error.code}`);
    return;
  }
  throw new Error(`expected ${status} ${code}`);
}

function assert(condition, message) { if (!condition) throw new Error(message); }

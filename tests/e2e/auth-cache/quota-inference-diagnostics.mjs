// Bounded failure-only observations. Raw tokens, policy sources, Envoy plugin
// configs and Authorino logs remain in memory and never enter the report.
export async function quotaInferenceDiagnostics({kubectl, person, adminPort}) {
  const result = {};
  const dump = await (await fetch('http://127.0.0.1:' + adminPort + '/config_dump',
    {signal: AbortSignal.timeout(5000)})).json();
  const listener = dump.configs.flatMap(c => c.dynamic_listeners ?? [])
    .map(l => l.active_state?.listener).find(l => l?.address?.socket_address?.port_value === 80);
  result.modelFilters = listener?.filter_chains.flatMap(c => c.filters ?? [])
    .flatMap(f => f.typed_config?.http_filters ?? []).map(f => f.name);
  result.quotaMatches = dump.configs.flatMap(c => c.ecds_filters ?? []).map(e => e.ecds_filter)
    .filter(e => e?.name?.includes('.aep-quota-')).map(e => ({name: e.name,
      rules: (JSON.parse(e.typed_config.config.configuration.value)._rules_ ?? []).map(r => ({
        routes: r._match_route_, domains: r._match_domain_, disabled: r._config_disable_,
      }))}));
  const grant = JSON.parse(await kubectl(['-n', 'aep-system', 'get', 'modelaccessgrant', 'access-' + person.binding, '-o', 'json']));
  const claims = JSON.parse(Buffer.from(person.tokens.modelAccessToken.split('.')[1], 'base64url'));
  result.grant = {disabled: grant.spec.disabled, validUntil: grant.spec.validUntil,
    tokenUnexpired: claims.exp * 1000 > Date.now(), subjectMatches: claims.sub === grant.spec.subject.id,
    sessionMatches: claims.session_id === grant.spec.binding.sessionId};
  result.authorino = [];
  for (const name of ['authorino-a', 'authorino-b']) {
    const logs = await kubectl(['-n', 'aep-system', 'logs', 'deployment/' + name, '--tail=200']);
    result.authorino.push({name,
      discoveryFailure: /failed to discovery openid connect configuration|missing openid connect configuration/.test(logs),
      signatureFailure: /failed to verify signature|failed to verify id token signature/.test(logs),
      connectionFailure: /connection refused|connection reset|i\/o timeout|context deadline exceeded/.test(logs),
      expiredCredential: /token is expired|token expired/.test(logs)});
  }
  return result;
}

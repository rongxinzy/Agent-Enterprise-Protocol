// Return bounded, configuration-free diagnostics; never serialize raw Envoy
// dumps because native provider plugin configurations can contain credentials.
export async function higressDiagnostics(kubectl) {
  try {
    const gatewayStats = await kubectl(['-n', 'higress-system', 'exec', 'deployment/higress-gateway', '--',
      'curl', '-s', 'http://127.0.0.1:15000/stats?filter=update_rejected|update_success|listener_manager.total_listeners']);
    const dump = JSON.parse(await kubectl(['-n', 'higress-system', 'exec', 'deployment/higress-gateway', '--',
      'curl', '-s', 'http://127.0.0.1:15000/config_dump']));
    const listenerStates = dump.configs.flatMap(c => c.dynamic_listeners ?? []).map(l => ({name: l.name,
      active: Boolean(l.active_state), rejected: Boolean(l.error_state)}));
    return {gatewayStats, listenerStates};
  } catch {
    return {gatewayStats: 'Unavailable'};
  }
}

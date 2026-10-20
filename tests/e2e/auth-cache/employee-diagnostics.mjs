// Bounded lifecycle diagnostics; never dump Secrets, configs or request bodies.
export async function employeeDiagnostics(kubectl) {
  const result = {};
  for (const [key, args] of [
    ['employeePods', ['-n', 'digital-employees', 'get', 'pods', '-o', 'wide']],
    ['employeeWarnings', ['-n', 'digital-employees', 'get', 'events', '--field-selector', 'type=Warning']],
    ['employeeOperator', ['-n', 'digital-employees', 'logs', 'deployment/de-operator', '--tail=40']],
    ['employeeObserver', ['-n', 'aep-system', 'logs', 'deployment/aep-workload-observer', '--tail=40']],
  ]) {
    try {result[key] = await kubectl(args, {timeout: 10_000});}
    catch {result[key] = 'Unavailable';}
  }
  return result;
}

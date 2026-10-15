// Resolve through the current Compose project, including stopped containers.
// Starting the exact container keeps its data and avoids a successful Compose
// command being mistaken for a dependency that actually resumed.
export async function startServiceContainer(service, {composeOutput, commandOutput}) {
  const output = await composeOutput('ps', '--all', '--quiet', service);
  const ids = output.split(/\r?\n/).map(id => id.trim()).filter(Boolean);
  if (ids.length !== 1 || !/^[a-f0-9]{12,64}$/.test(ids[0])) {
    throw new Error(`Expected one existing container for runtime service ${service}`);
  }
  const [id] = ids;
  await commandOutput('docker', ['start', id]);
  const running = await commandOutput('docker', ['inspect', '--format', '{{.State.Running}}', id]);
  if (running !== 'true') {
    throw new Error(`Runtime service ${service} did not start: running=${running}`);
  }
  return id;
}

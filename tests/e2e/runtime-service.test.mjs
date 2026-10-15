import assert from 'node:assert/strict';
import {test} from 'node:test';
import {startServiceContainer} from './runtime-service.mjs';

const id = 'a'.repeat(64);

test('restores the stopped dependency selected from the current Compose project', async () => {
  let running = false;
  const started = [];
  const result = await startServiceContainer('postgres', {
    async composeOutput(...args) {
      assert.deepEqual(args, ['ps', '--all', '--quiet', 'postgres']);
      return `${id}\r\n`;
    },
    async commandOutput(executable, args) {
      assert.equal(executable, 'docker');
      assert.equal(args.at(-1), id);
      if (args[0] === 'start') {
        started.push(args[1]);
        running = true;
        return id;
      }
      assert.deepEqual(args, ['inspect', '--format', '{{.State.Running}}', id]);
      return String(running);
    },
  });
  assert.equal(result, id);
  assert.equal(running, true);
  assert.deepEqual(started, [id]);
});

test('a successful start command with a still-stopped container fails recovery', async () => {
  await assert.rejects(startServiceContainer('postgres', {
    composeOutput: async () => id,
    commandOutput: async (_, args) => args[0] === 'start' ? id : 'false',
  }), /postgres did not start: running=false/);
});

test('missing, ambiguous, or invalid container IDs never issue a start', async () => {
  for (const output of ['', `${id}\n${'b'.repeat(64)}`, '--all', 'not-a-container']) {
    await assert.rejects(startServiceContainer('postgres', {
      composeOutput: async () => output,
      commandOutput: async () => assert.fail('must not start an unverified container'),
    }), /Expected one existing container/);
  }
});

test('engine errors propagate rather than being hidden by a readiness retry', async () => {
  const failure = new Error('engine rejected start');
  await assert.rejects(startServiceContainer('rustfs', {
    composeOutput: async () => id,
    commandOutput: async () => { throw failure; },
  }), error => error === failure);
});

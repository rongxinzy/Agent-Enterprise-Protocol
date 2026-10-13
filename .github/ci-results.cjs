'use strict';

// A skipped required check can satisfy branch protection. Always run the final
// summary and reject missing, skipped, cancelled or failed callers.
function verifyNeeds(needs, required) {
  if (JSON.stringify(Object.keys(needs).sort()) !== JSON.stringify([...required].sort()))
    throw new Error('CI dependency inventory differs from the trusted configuration.');
  for (const name of required) {
    if (needs[name]?.result !== 'success')
      throw new Error(`Required CI ${name}: ${needs[name]?.result ?? 'missing'}`);
  }
}

module.exports = { verifyNeeds };

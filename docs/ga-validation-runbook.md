# GA Validation Runbook

The release candidate keeps load and soak validation separate from the default
Compose E2E suite. Run it against a disposable or production-like deployment;
the script does not send credentials or model prompts.

```sh
AEP_LOAD_BASE_URL=http://localhost:8080 \
AEP_LOAD_DURATION_SECONDS=300 \
AEP_LOAD_CONCURRENCY=16 \
AEP_LOAD_MAX_ERROR_RATE=0.01 \
npm run test:e2e:load
```

The command probes `/healthz` by default and prints JSON containing request
count, failures, error rate, and p95 latency. Set `AEP_LOAD_ENDPOINT` to a
read-only authenticated endpoint only when the test environment explicitly
provides the required headers through an approved test wrapper. A run is
successful only when the error rate stays below the configured threshold.

Record the output, deployment image digests, database/object-store versions,
and the test window as release evidence. This harness is one input to the GA
gate; it does not replace an external security review or a backup and disaster
recovery rehearsal.

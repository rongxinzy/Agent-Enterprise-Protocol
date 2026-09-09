# Offline License E2E Boundary

The complete offline License E2E is a local-only verification. Run it from a
controlled workstation or the customer's air-gapped network:

```bash
npm ci
npm run test:e2e:offline-license
```

The test deliberately refuses to run when `CI=true` unless
`AEP_ALLOW_OFFLINE_LICENSE_E2E=1` is explicitly set. CI runs only the static
`npm run license:boundary:check`; it never receives the License signer, a
License private key, a signer checkout, or a customer License.

The committed fixtures contain one test License envelope and its Ed25519
public verification key. The License signing private key used to produce the
fixture was never persisted in this repository. The E2E creates only an
ephemeral AEP JWT test seed in process memory for the local service.

The Compose overlay mounts the fixtures read-only and places PostgreSQL,
MinIO, and Control Service on an internal Docker network. It probes that the
network has no external egress, checks that activation sends `{}` rather than
License material, verifies activation after a service restart, rejects a
tampered or deployment-mismatched License at startup, and scans service logs
for License or key material.

Do not add customer License files, signer paths, private keys, signing
configuration, or unredacted signing logs to this repository. Keep those
artifacts in the approved offline signing environment.

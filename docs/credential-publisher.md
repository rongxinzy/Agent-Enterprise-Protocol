# Direct gateway credential delivery

The opt-in credential publisher closes the gap between Admin Console Credential
storage and gateway consumption without requiring an external secret backend:

`Console → AEP sealed PostgreSQL Credential → publisher → Kubernetes Secret → gateway Operator → Higress`

The HTTP control service still has no Secret write permission. The publisher is
a separate process/service account, not an HTTP endpoint or an addition to the
gateway Operator. It shares AEP's AES-GCM/keyring implementation. CRs, API
responses and logs never receive provider plaintext. The existing renderer reads
the Secret and inserts the provider key into Higress configuration as before;
this does not remove the sensitivity of the rendered Higress objects.

## Eligibility and lifecycle

Only enabled `server_only` API-key Credentials referenced by at least one enabled
`gateway` model using `openai-compatible` or `anthropic` are supplied. Other
Credentials, including Agent-delivered credentials, stay out of this namespace.
The source and model references are always scoped to the configured deployment.

- Target: `aep-credentials/aep-credential-<credentialId>`, data key `api-key`.
- Creating a model binding supplies its eligible Credential. Rotating the
  Credential updates the owned Secret without changing its name.
- Disabling/deleting the Credential, changing it to Agent delivery, or removing
  its last eligible model binding removes only the publisher-owned Secret.
  A shared credential remains while another eligible model still references it.
- Ownership labels, exact identity annotations, absence of owner references and
  optimistic resourceVersion/UID checks protect against accidental takeover.
  Existing ESO or manually managed Secrets are refused, not adopted.
- Database errors never mean "credential absent". API/decryption failures report
  unhealthy synchronization, retain retryable state and retry from current DB
  state. An error does not block processing unrelated credentials.
- Missing target Secrets and drift are repaired on restart and subsequent
  passes. No plaintext queue or additional database migration is introduced.

The worker uses controller-runtime leader election. Default deployment is one
replica, with a non-overlapping rolling update; existing gateway traffic does
not depend on this process remaining online between changes. Readiness requires
a complete successful pass; liveness measures process health separately.

Synchronization is **eventually consistent**: by default a new pass starts five
seconds after the previous pass. Pages contain at most 100 objects and operations
have ten-second deadlines. Large inventories, database/API outages and conflicts
can extend this delay. This is a periodic reconciliation worker, not a
transactional outbox or an immediate Kubernetes commit inside an HTTP request.
Credential API success still means "saved to DB", not "already effective in
Higress". Secret delivery, route publication, Operator Applied status and actual
data-plane effectiveness are different checkpoints. A rotation or revocation
also needs the Operator/reconciler to refresh the rendered configuration.

## Install and privileges

1. Start the normal AEP backend and gateway deployment. Ensure `aep-system` and
   `aep-credentials` exist. The publisher never runs migrations or bootstrap.
2. Provision `aep-system/aep-credential-publisher-config` using the deployment's
   protected Secret process. It contains `database-url` for a dedicated database
   identity and `credential-keyring` matching the control service's full keyring,
   including retained historical keys. Do not mount the control service's whole
   Secret bundle or put credentials in build inputs.
3. Grant the database identity only `CONNECT`, schema `USAGE`, and `SELECT` on
   `credentials` and `models`; apply deployment-specific network/access controls.
   The process holds decryption capability: this is a trusted secret-delivery
   component. The application query filters are not database row-level security.
4. Set the manifest deployment ID to the installation's AEP deployment ID and
   render `kubectl kustomize deploy/kubernetes/credential-publisher` for review.
5. Build with `KO_DOCKER_REPO=<registry> ko build ./services/control-service/cmd/credential-publisher`
   and replace the `ko://` reference with its immutable image, or use `ko apply`.
   Use an approved non-root static base with CA certificates. Verify the image's
   entrypoint and UID before rollout.

The role can read/create/update/delete Secrets only in `aep-credentials`; it
cannot write Higress resources or read the control service's bootstrap Secrets.
Secret list/write permissions apply to the whole credential namespace, not to
individual label-selected objects. Ownership checks are application safeguards,
not a substitute for RBAC or isolation of unrelated secrets. Lease create and
get/update of the fixed publisher Lease live in `aep-system`.

Configuration:

| Variable | Meaning |
| --- | --- |
| `AEP_CREDENTIAL_PUBLISHER_DEPLOYMENT_ID` | Required installation identity |
| `AEP_CREDENTIAL_PUBLISHER_DATABASE_URL_FILE` | File containing the dedicated PostgreSQL DSN |
| `AEP_CREDENTIAL_PUBLISHER_DATABASE_URL` | Alternative DSN; mutually exclusive with the file |
| `AEP_CREDENTIAL_MASTER_KEY_FILE` | Required AEP keyring file |
| `AEP_CREDENTIAL_PUBLISHER_INTERVAL` | Pass interval, `1s`–`1h`, default `5s` |
| `KUBECONFIG` | Local development only; Pods use their service account |

Verified database TLS is required unless `AEP_ENVIRONMENT` is explicitly
`development` or `test`. Errors are sanitized; database SQL logging is disabled.
Readiness is `/readyz`, liveness `/healthz`, on port 8095. No Secret-serving HTTP
API is exposed. The network baseline permits DNS, database 5432 and API 443;
restrict destinations and adapt ports in the reviewed environment overlay.

## Existing ESO deployments and rollback

ESO remains useful for enterprises that already have an external secret manager,
and can still supply AEP's deployment/bootstrap Secrets. This optional package
does not install Vault/ESO or remove existing ExternalSecrets.

Choose **one owner per provider Secret**. To move from ESO, stop the corresponding
external provisioning and perform a reviewed migration of the target Secret.
Deleting an ExternalSecret with `creationPolicy: Owner` also deletes its Secret;
account for the gap before the publisher recreates it. The publisher deliberately
does not silently adopt the old object. Test this migration outside production.

Stopping/uninstalling the publisher leaves delivered Secrets intact, so rollback
does not revoke every upstream credential at once. Before switching back to ESO,
explicitly migrate/remove publisher-owned targets; do not run competing writers.
The underlying Credential and its DB keyring remain the recovery source.

## Local acceptance

Run `node tests/e2e/credential-publisher.mjs` against a running local AEP backend
and publisher. Supply `ZHIYUAN_AEP_BASE_URL`, `ZHIYUAN_AEP_DEPLOYMENT_ID`,
`ZHIYUAN_AEP_ADMIN_USERNAME`, `ZHIYUAN_AEP_ADMIN_PASSWORD`, `KUBECONFIG`, and
`ZHIYUAN_AEP_KUBE_CONTEXT` through the environment. The script requires a loopback
API and explicit `kind-` context. It creates disposable mock Credentials/models,
verifies automatic delivery/rotation/revocation/reference sharing and exclusion
of `deliveryMode: client`, then deletes its fixtures and logs out. It neither
invokes a cloud provider nor provisions the target Secret itself. This opt-in
scenario is not part of the default self-contained Compose suite.

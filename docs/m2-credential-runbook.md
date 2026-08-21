# AEP M2 Credential Runbook

M2 adds encrypted Credential management to the control service. Administrators create, rotate, disable, assign, and delete Credential records through the management API or `aepctl`. Agents can discover and resolve only enabled Credentials with `deliveryMode: agent` that match their enterprise, organization, user, or Agent identity.

`server_only` Credentials remain control-plane metadata and cannot be listed or resolved by an Agent. Successful resolve responses include `Cache-Control: no-store`, and every resolve decision is recorded without secret material in `credential_resolution_audit`.

## Master Key Configuration

Credential APIs are enabled only when exactly one master-key source is configured:

- `AEP_CREDENTIAL_MASTER_KEY_BASE64`: a base64-encoded 32-byte AES-256 key.
- `AEP_CREDENTIAL_MASTER_KEY_FILE`: an absolute path to a key or keyring file.

Generate a production key with a cryptographically secure generator, for example:

```sh
openssl rand -base64 32
```

A key file may contain that base64 value directly. For controlled master-key rotation, use a JSON keyring and retain old keys while their ciphertext still exists:

```json
{
  "activeKeyId": "credential-key-2026-09",
  "keys": {
    "credential-key-2026-08": "BASE64_32_BYTE_OLD_KEY",
    "credential-key-2026-09": "BASE64_32_BYTE_ACTIVE_KEY"
  }
}
```

The file provider reloads the keyring for each cryptographic operation. New creates and rotations use `activeKeyId`; existing records use their stored key ID. Do not remove an old key until every Credential encrypted by it has been rotated. Changing the single environment key without first rotating stored Credentials makes those records unreadable.

The default Compose file contains a development-only key. Override it for every non-local deployment.

## CLI Workflow

Start the local stack and configure administrator authentication:

```sh
npm run compose:up
export AEPCTL_PASSWORD=change-this-admin-password
```

Create and assign an Agent-deliverable API key without placing the secret in command history:

```sh
export AEPCTL_CREDENTIAL_VALUE='provider-secret'
go run ./cmd/aepctl credential create --name provider --service example --delivery-mode agent
unset AEPCTL_CREDENTIAL_VALUE

go run ./cmd/aepctl credential assign \
  --credential-id CREDENTIAL_ID --subject-type user --subject-id USER_ID
```

Inspect metadata, disable access, rotate, and revoke an assignment:

```sh
go run ./cmd/aepctl credential list
go run ./cmd/aepctl credential show --credential-id CREDENTIAL_ID
go run ./cmd/aepctl credential update --credential-id CREDENTIAL_ID --enabled=false
AEPCTL_CREDENTIAL_VALUE='replacement-secret' go run ./cmd/aepctl credential rotate --credential-id CREDENTIAL_ID
go run ./cmd/aepctl credential assignments
go run ./cmd/aepctl credential revoke --assignment-id ASSIGNMENT_ID
```

Use `--value-file` when the secret is already mounted as a file. `--value` is supported for local automation but can be visible in process listings and shell history.

## Reference Agent

The Node reference Agent synchronizes Credential metadata before each use and resolves material only on demand. Values stay in process memory for at most 30 seconds, or until server expiry, rotation, revocation, or process shutdown. They are never written to SQLite, telemetry, logs, or command output.

```sh
export AEP_USERNAME=agent-user AEP_PASSWORD=agent-password AEP_AGENT_ID=agent-id
export AEP_CREDENTIAL_ID=credential-id
export AEP_CREDENTIAL_URL=https://service.example.test/protected
npm run start --workspace @aep/example-node-agent -- credential
```

The value is sent only as the Bearer header. Output contains Credential ID, service, and HTTP status. Set a shorter positive `AEP_CREDENTIAL_CACHE_MS` when needed.

## Validation

Run the focused Credential integration scenario:

```sh
npm run test:e2e:m2-control
npm run test:e2e:m2-agent
```

The control and Agent scenarios validate authorization, server-only isolation, encryption, rotation and revocation convergence, restart recovery, audit and CLI redaction, and absence of Credential material in SQLite, telemetry, and process output.

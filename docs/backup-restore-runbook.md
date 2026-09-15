# Backup And Restore Rehearsal

The backup rehearsal validates that the PostgreSQL control-plane records and
the MinIO Skill objects can be restored together into an isolated deployment.
It is a disposable integration test and never touches the default `aep-m0`
Compose project or its volumes.

Run it locally from the repository root:

```sh
npm ci
npm run test:e2e:backup-restore
```

The scenario creates a temporary user, Skill, published version, and user
assignment. It stops application writes, takes a PostgreSQL custom-format dump
and a MinIO data-volume archive, restores both into a second Compose project,
then verifies administrator session/JWKS continuity, Skill metadata, the user
manifest, and the downloaded package checksum. Both projects and all temporary
volumes are removed in the final cleanup path.

The test uses `AEP_BACKUP_SOURCE_PORT`, `AEP_BACKUP_RESTORE_PORT`,
`AEP_BACKUP_SOURCE_MINIO_PORT`, and `AEP_BACKUP_RESTORE_MINIO_PORT` when the
default disposable ports are unavailable. Do not point these projects at an
existing production database or object store.

This rehearsal is evidence for the GA gate, not a substitute for the
deployment's scheduled PostgreSQL/MinIO backups, secret-provider backups, or
an organization-approved recovery-time and recovery-point objective.

## Operational tools

The repository also includes operational scripts for a coordinated backup. The
backup briefly stops `control-service` and MinIO, writes a PostgreSQL custom dump
and a MinIO data-volume archive to one directory, and restarts the services:

```sh
npm run ops:backup -- --project aep-m0 --output-dir backups/20260909
```

The output directory is forced to mode `0700`, and every artifact and manifest
is forced to mode `0600`. An unencrypted backup contains `postgres.dump`,
`minio-data.tgz`, and a v2 `manifest.json` with byte counts and SHA-256 values.
Treat the directory as sensitive even though Credential values remain encrypted
at the application layer.

For offline envelope encryption, create a base64-encoded 32-byte key-encryption
key outside the repository and protect it in the deployment Secret system:

```sh
umask 077
openssl rand -base64 32 > /secure/aep-backup.key
npm run ops:backup -- --project aep-m0 --output-dir backups/20260909 \
  --encryption-key-file /secure/aep-backup.key
```

The script creates a random data key for each backup, wraps it with the supplied
key-encryption key, and encrypts both artifacts with AES-256-GCM. The output
files are then named `postgres.dump.enc` and `minio-data.tgz.enc`. The key path
may instead be supplied through `AEP_BACKUP_ENCRYPTION_KEY_FILE`; key material
is never written to the manifest or command output. Back up the key separately.

MinIO volume archiving uses the locally preloaded `alpine:3.20` image by
default. A different organization-approved, explicitly tagged or digest-pinned
image must be passed with `--helper-image` to both backup and restore. Helper
containers always run with `--pull never`, and restore rejects a manifest whose
helper image differs from the locally selected image. The scripts do not back
up database passwords, deployment Secrets, License files, signing seeds,
Credential keyrings, or provider keys held by the external Secret system.

Restore replaces the target project's database and MinIO volume and therefore
requires an explicit confirmation:

```sh
npm run ops:restore -- --project aep-m0 --input-dir backups/20260909 --confirm yes
```

Supply the same key-encryption key when restoring an encrypted backup:

```sh
npm run ops:restore -- --project aep-m0 --input-dir backups/20260909 \
  --encryption-key-file /secure/aep-backup.key --confirm yes
```

Stop all writes before restoring and rehearse in an isolated project first. The
restore command rechecks every SHA-256, starts Compose with `--no-build --pull
never`, authenticates every encrypted artifact before passing plaintext to a
restore process, rejects symlinks and encryption downgrades, and waits for
`/readyz`. The tool still accepts legacy v1 unencrypted backups. It does not
delete or roll back existing data; on failure, keep the target stopped and
follow the organization's recovery procedure.

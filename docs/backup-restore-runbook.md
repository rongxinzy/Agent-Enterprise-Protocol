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

The directory contains `postgres.dump`, `minio-data.tgz`, and a
`manifest.json` with byte counts and SHA-256 values. MinIO volume archiving uses
`alpine:3.20` by default; preload an organization-approved equivalent and pass
`--helper-image` when required. The scripts never back up database passwords,
deployment Secrets, License material, or provider credentials.

Restore replaces the target project's database and MinIO volume and therefore
requires an explicit confirmation:

```sh
npm run ops:restore -- --project aep-m0 --input-dir backups/20260909 --confirm yes
```

Stop all writes before restoring and rehearse in an isolated project first. The
restore command rechecks every SHA-256, starts Compose with `--no-build --pull
never`, and waits for `/readyz`. It does not delete or roll back existing data;
on failure, keep the target stopped and follow the organization's recovery
procedure.

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

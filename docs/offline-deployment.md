# Offline Deployment Bundle

The repository can export a self-contained Docker image bundle for a controlled
air-gapped installation. The bundle contains Compose inputs, locally saved image
archives, immutable image IDs/digests, and SHA-256 checksums. It never contains
PostgreSQL data, MinIO data, provider credentials, License private keys, signing
seeds, or customer configuration.

## Export

Build the images on a connected build host first, then run:

```sh
npm run compose:up
npm run offline:bundle -- --output-dir release/offline-base --profile base
npm run compose:gateway:up
npm run offline:bundle -- --output-dir release/offline-gateway --profile gateway
```

The command fails if any declared image is missing locally. Each archive is
listed in `manifest.json` and `SHA256SUMS`. Keep the manifest and image archives
together during transfer.

## Air-gapped install

On the target host, run the dependency-free installer bundled with the release.
It verifies every archive against both checksum sources, loads the images, starts
Compose without pulling or building, and waits for control-service readiness:

```sh
node install-offline-bundle.mjs --project aep-offline --port 8080
```

Use `node install-offline-bundle.mjs --dry-run` to inspect the actions without
changing Docker state. For a base-only bundle the installer automatically omits
`gateway.yaml`. The generated `offline.yaml` removes build contexts and pins the loaded AEP service images. PostgreSQL,
MinIO, deployment Secrets, License material, and provider credentials remain
deployment inputs and must be provisioned separately through the approved
offline Secret process.

This is an image/install bundle, not an upgrade mechanism. Before upgrading,
take the coordinated PostgreSQL and MinIO backup described in
`backup-restore-runbook.md`, verify schema compatibility, then load the new
bundle and roll the services according to `production-runtime.md`.

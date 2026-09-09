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

On the target host, verify the checksums with the organization's trusted local
tool, load every archive, and start Compose without `--build`:

```sh
for archive in images/*.tar; do docker load --input "$archive"; done
docker compose -p aep-offline \
  -f deploy/compose/compose.yaml \
  -f deploy/compose/gateway.yaml \
  -f deploy/compose/offline.yaml up -d
```

For a base-only bundle omit `gateway.yaml`. The generated `offline.yaml`
removes build contexts and pins the loaded AEP service images. PostgreSQL,
MinIO, deployment Secrets, License material, and provider credentials remain
deployment inputs and must be provisioned separately through the approved
offline Secret process.

This is an image/install bundle, not an upgrade mechanism. Before upgrading,
take the coordinated PostgreSQL and MinIO backup described in
`backup-restore-runbook.md`, verify schema compatibility, then load the new
bundle and roll the services according to `production-runtime.md`.

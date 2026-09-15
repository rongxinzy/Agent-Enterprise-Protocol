# Offline Deployment Bundle

The repository can stage a self-contained Docker image bundle for a controlled
air-gapped installation. The bundle contains Compose inputs, locally saved image
archives, immutable image IDs/digests, and SHA-256 checksums. It never contains
PostgreSQL data, MinIO data, provider credentials, License private keys, signing
seeds, offline release private keys, or customer configuration.

## Export

Build the images on a connected build host first, then run:

```sh
npm run compose:up
npm run offline:bundle -- --output-dir release/offline-base --profile base
npm run compose:gateway:up
npm run offline:bundle -- --output-dir release/offline-gateway --profile gateway
```

The command fails if any declared image is missing locally. It writes
`manifest.json` and `SHA256SUMS`, then exits with `awaiting-signature`. Every
payload file is listed in the manifest, and the checksum file covers the
manifest, installer, Compose inputs, documentation, fixtures, and image
archives.

The approved local signer, which is not part of this repository or cloud CI,
must create the detached Ed25519 signature over the exact `SHA256SUMS` bytes:

```sh
openssl pkeyutl -sign -rawin \
  -inkey /secure/offline-release.private.pem \
  -in release/offline-base/SHA256SUMS \
  -out release/offline-base/SHA256SUMS.sig
```

Repeat for each profile, validate it with the separately held public key, and
only then package the directory. Never publish the unsigned staging directory.

## Air-gapped install

Provision the trusted Ed25519 public key on the target through a different
channel from the bundle. Before executing any bundled code, use trusted host
tools to authenticate the checksum file and validate every payload:

```sh
openssl pkeyutl -verify -pubin \
  -inkey /etc/aep/offline-release.pub.pem \
  -rawin -in SHA256SUMS -sigfile SHA256SUMS.sig
sha256sum --check SHA256SUMS
```

Then run the dependency-free installer. It repeats the signature and full-file
checks, loads images, starts Compose without pulling or building, and waits for
control-service readiness:

```sh
node install-offline-bundle.mjs \
  --trusted-public-key /etc/aep/offline-release.pub.pem \
  --project aep-offline --port 8080
```

The installer rejects a public key located inside the bundle; such a key is not
an independent trust anchor. Add `--dry-run` to inspect the actions without
changing Docker state. For a base-only bundle the installer automatically omits
`gateway.yaml`. The generated `offline.yaml` removes build contexts and pins the loaded AEP service images. PostgreSQL,
MinIO, deployment Secrets, License material, and provider credentials remain
deployment inputs and must be provisioned separately through the approved
offline Secret process.

This is an image/install bundle, not an upgrade mechanism. Before upgrading,
take the coordinated PostgreSQL and MinIO backup described in
`backup-restore-runbook.md`, verify schema compatibility, then load the new
bundle and roll the services according to `production-runtime.md`.

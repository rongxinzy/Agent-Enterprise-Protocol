# Foundation Release Artifacts

Pushing an `aep-v<package-version>` tag on a commit contained in `main` runs the
complete release gate and publishes an AEP foundation GitHub Release. The
release contains:

- base and gateway offline Compose bundles;
- a CycloneDX source SBOM;
- CycloneDX SBOMs for control-service, gateway-authorizer, and
  gateway-reconciler images;
- `release-manifest.json`, which binds artifact names and SHA-256 values to the
  exact Git commit and protocol version;
- `SHA256SUMS`, which also covers the release manifest.

The versioned AEP images are embedded in the offline bundles. Third-party images
used by the local Compose gateway profile remain identified by image reference
and digest in each Bundle's `manifest.json`. The gateway Bundle is an
integration and air-gap test topology; it does not turn `higress-standalone`
into an approved production topology. Both Compose bundles are pinned to the
development profile and loopback-only host ports. They validate offline image
transfer and integration; customer production deployment uses the Kubernetes
baseline and externally supplied Secrets.

Download all release files and verify them before transferring the Bundle:

```sh
sha256sum --check SHA256SUMS
```

The cloud workflow does not receive a License signing key, customer artifact
signing key, deployment Secret, provider credential, or customer License. When
a customer requires a signed delivery, the approved local signing environment
signs the reviewed `release-manifest.json` or customer packaging manifest. Keep
that signer, its private key, and its logs outside this repository and CI.

Publication of these artifacts provides verifiable release evidence. It does
not by itself complete the external security review or customer acceptance.

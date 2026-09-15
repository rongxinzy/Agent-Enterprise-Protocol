# Foundation Release Artifacts

Pushing an `aep-v<package-version>` tag on a commit contained in `main` runs the
complete release gate and publishes an AEP foundation GitHub Release. The
release contains:

- a CycloneDX source SBOM;
- CycloneDX SBOMs for control-service, gateway-authorizer, and
  gateway-reconciler images;
- `release-manifest.json`, which binds artifact names and SHA-256 values to the
  exact Git commit and protocol version;
- `SHA256SUMS`, which also covers the release manifest.

The cloud release workflow builds base and gateway offline directories only to
validate their unsigned signing inputs. It does not package or publish them:
the production offline signing key is intentionally unavailable to GitHub CI.
An approved local release workstation signs `SHA256SUMS`, validates the complete
bundle against an out-of-band public key, and packages the customer delivery.
See `offline-deployment.md` for that flow.

Download all public foundation release files and verify them before use:

```sh
sha256sum --check SHA256SUMS
```

The cloud workflow does not receive a License signing key, offline bundle
signing key, deployment Secret, provider credential, or customer License. Keep
the offline signer, its private key, and its logs outside this repository and
CI. Never distribute an unsigned CI staging directory as an installable bundle.

Publication of these artifacts provides verifiable release evidence. It does
not by itself complete the external security review or customer acceptance.

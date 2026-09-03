# Local Enterprise License Signing Boundary

The enterprise License signer is an offline, locally controlled component. It
is deliberately not part of the AEP repository, the Control Service image, the
Admin Console, or any cloud build job.

## Responsibilities

- The signer holds the production License signing private key and runs only in
  the organization's controlled signing environment.
- A release operator creates a signed License envelope locally and transfers
  only the resulting License and its public verification key through the
  organization's approved distribution channel.
- The Zhiyuan enterprise client verifies the License locally, then exchanges
  License evidence with `POST /aep/v1/agent/activation`.
- Control Service re-verifies the complete License envelope with its configured
  vendor public keys, checks deployment and entitlement state, and issues a
  short-lived entitlement JWT. It never receives a License private key and
  never performs License signing.
- In an air-gapped deployment, mount the License and trusted public-key file into
  Control Service and place the same License in the enterprise client resources.
  Activation then completes entirely inside the customer network.

## Repository boundary

Keep the signer checkout outside this repository, for example:

```text
D:\rxzy\zhiyuan-license-signer\
```

Do not copy signer source, private keys, signing configuration, or unredacted
signing logs into the AEP checkout. The repository ignores conventional local
signer directories and private-key artifact names as an additional guard, but
the operator remains responsible for checking `git status` before every push.

Public verification material may be embedded in a client release or served by
the approved License distribution system. It must not be confused with the
private signing key.

## Rotation and recovery

Keep old public keys available until every License signed with the previous key
has expired. Back up the private key and signing metadata in the organization's
offline secret-management system, separately from PostgreSQL, MinIO, and the
Credential keyring. AEP backup and recovery procedures must restore matching
verification material before reactivating enterprise clients.

## Release gate

Before publishing an AEP or Zhiyuan enterprise build:

1. Verify that no signer path or private-key artifact is tracked by Git.
2. Verify that CI has no access to the signing environment or private key.
3. Verify the client can validate a known test License and reject a modified
   envelope before calling the activation endpoint.
4. Verify the activation response contains only a short-lived entitlement JWT.

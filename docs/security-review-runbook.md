# External Security Review Runbook

An external review is required before promoting the AEP foundation from release
candidate to general availability. Automated dependency and CodeQL scans are
continuous controls; they are inputs to, not substitutes for, an independent
review.

## Review target

Freeze one commit from `main` and record its 40-character SHA. Generate the
foundation Release for that commit and provide the reviewer with the source,
offline Bundle manifests, source/image SBOMs, deployment manifests, OpenAPI
bundles, and the public test evidence. Customer credentials, License private
keys, signing seeds, production data, and signer logs are never review inputs.

The minimum component scope is:

- Node SDK transport, session restoration, token rotation, and error handling;
- control-service authentication, RBAC, assignments, events, telemetry,
  Credential storage/delivery, License enforcement, and administrative APIs;
- gateway-authorizer JWT/JWKS validation, model-scope enforcement, entitlement
  checks, request limits, streaming, and upstream failure behavior;
- gateway-reconciler Kubernetes permissions, desired-state validation, Secret
  references, and cross-deployment isolation;
- reference Agent inbox/outbox, Skill ZIP extraction, checksum validation,
  Credential no-store behavior, and user-topic event delivery;
- Compose and Kubernetes defaults, network boundaries, probes, logs, backup,
  restore, offline install, and release supply chain.

The closed Zhiyuan enterprise extension and Admin Console require their own
review in that repository. Their findings cannot be represented as AEP findings
unless the exact reviewed source and commit are included in the assessment.

## Required attack classes

At minimum, assess OWASP ASVS/API Security Top 10 concerns, authentication
bypass, session replay, refresh-token races, IDOR across users/roles/teams,
privilege escalation, JWT confusion and stale JWKS, SSRF through model/provider
configuration, request smuggling and streaming resource exhaustion, ZIP path or
link traversal, Credential exfiltration, audit/log secret leakage, SQL/object
identifier injection, event topic leakage, replay/idempotency failure, License
tampering, unsafe production defaults, and dependency/container vulnerabilities.

## Evidence and exit criteria

The assessor must return a report that identifies the reviewer, review dates,
methodology, exact commit, component scope, environment, findings, severity
method, remediation, and retest results. Store the confidential report in the
approved evidence system; the repository may contain only a non-sensitive
attestation and the report SHA-256.

GA promotion requires all of the following:

1. No open critical or high findings.
2. Every medium finding has an accepted owner, mitigation, and due date.
3. Remediated critical/high findings were independently retested.
4. `npm run release:check`, the security workflow, offline install, backup and
   restore, and the product integration gate pass on the reviewed commit.
5. The published release manifest identifies that same commit and all released
   artifacts pass `SHA256SUMS` verification.
6. A designated release owner records the go/no-go decision; the implementation
   must remain `release-candidate` and 95% until this evidence exists.

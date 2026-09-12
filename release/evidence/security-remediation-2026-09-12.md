# Security Remediation Evidence - 2026-09-12

Status: engineering verification complete; independent retest pending.

This record documents engineering remediation and CI evidence. It is not an
external attestation and does not replace the independent security retest
required by the GA gate.

## Remediated Boundaries

| Pull request | Commit | Boundary |
| --- | --- | --- |
| #109 | `a4588b8cdeb5b6281c9ee809f24c9b8655d44d8b` | License entitlement enforcement |
| #110 | `76c1057e44401b8a3c911b3e59ebac58f23158ee` | Gateway authorization hardening |
| #111 | `61a594f198647ca002e62b26e09d4a58acf2a781` | Production deployment security baseline |
| #148 | `d1dfb9dcab20f111c0e8fa5bc214a5fa35165329` | Delegated Team grant authorization |
| #149 | `fd0814926de90780a9484191aaee14719f330724` | Active-session and online-user entitlement binding |
| #150 | `78da02889e14016a80feb5e51ed68bd68023ed5c` | SDK cross-origin redirect rejection |
| #151 | `ab46f19ee6f938bc7f2168b2af4aef4aa185163f` | Source-aware password-login throttling and trusted proxy handling |

## Verification Evidence

The repository's full CI gate completed successfully for the most recent
authorization, session, SDK transport, and login-throttling boundaries:

| Pull request | Full CI run |
| --- | --- |
| #148 | `34684270108` |
| #149 | `34685343169` |
| #150 | `34685843444` |
| #151 | `34686937762` |

These runs cover the contract and SDK gates, Go tests and race checks, Compose
end-to-end scenarios, the Kubernetes data-plane gate, backup/restore rehearsal,
and the aggregate release gate configured by the repository at each commit.

## Open Hardening And Release Conditions

- Replace or constrain Compose development defaults before any exposed deployment.
- Add stricter Skill identifier and version validation, including object-key boundaries.
- Complete the formal independent security retest against frozen post-remediation commits.
- Sign customer artifacts only in the approved local signing environment.

Release status therefore remains `release-candidate` at 95%.

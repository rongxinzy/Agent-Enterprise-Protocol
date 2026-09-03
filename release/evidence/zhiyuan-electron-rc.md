# Zhiyuan Electron Release-Candidate Evidence

This evidence closes the AEP product-client integration gate for the password-only
enterprise profile. It records the product-side checks without including source from
the closed enterprise repository, credentials, tokens, provider responses, or logs.

## Source and command

- Repository: `rongxinzy/zhiyuanAaaS`
- Merged PR: `#52`
- Merge commit: `0e1fa6f`
- Verification command: `npm run verify:electron-rc`
- Package workflow: `.github/workflows/package-windows.yml`

## Automated checks

- Enterprise extension host registration and disposal
- Enterprise session login projection
- Exclusive managed model projection
- OpenAI-compatible gateway fail-closed behavior
- Provider credential and password redaction in projected state
- Packaged Electron asset comparison when `ZHIYUAN_ELECTRON_PACKAGE_DIR` is supplied

The AaaS CI run for PR #52 passed. The existing AEP client and gateway E2E suites
continue to cover non-streaming, SSE, reasoning content, tool continuation,
model-token authorization, and telemetry redaction. A disposable real-client pilot
was completed separately with the Zhiyuan password profile; no provider API key or
session secret is part of this record.

The remaining GA validation gate is intentionally still open for load/soak testing,
external security review, backup and disaster-recovery rehearsal, SBOM publication,
and signed artifacts.

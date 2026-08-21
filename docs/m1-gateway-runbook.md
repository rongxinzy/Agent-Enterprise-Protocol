# AEP M1 Higress Gateway Runbook

M1 adds an OpenAI-compatible model data plane to the M1 control plane. Higress
2.2.4 performs provider routing, model rewriting, streaming, and server-side
provider credential injection. A small AEP gateway authorizer in front of
Higress validates the AEP model token and its requested model grant locally.

## Architecture

```text
Agent -- model JWT --> gateway-authorizer --> Higress AI Proxy --> provider
             JWKS cache ^                         | server-only API key
                        Control Service            v
```

Only `gateway-authorizer` is published to the host. Higress and the mock
provider remain on the Compose network. The authorizer strips the client
`Authorization` header before forwarding. Higress then injects the configured
provider credential, so neither the model catalog nor the inference response
exposes provider secrets.

The authorizer accepts `POST /v1/*` inference requests with a JSON `model`
field. It verifies Ed25519 signature and `kid` against cached JWKS, then checks
`iss`, `aud=model-gateway`, `exp`, `iat`, `token_use=model`, AEP identity claims,
and membership of the requested model in `model_scopes`. JWKS refresh is not a
per-request authorization decision and the signing private key is never shared
with the data plane.

Higress native JWT plugins do not, in v2.2.4, enforce all of the AEP-specific
claims or compare an array claim with the model in an OpenAI request body. That
is why this narrowly scoped authorizer is required in addition to AI Proxy.

## Local Demo

Prerequisites are Node.js 24, Go 1.26, Docker Desktop, and Docker Compose.

```bash
npm ci
npm run build
npm run compose:gateway:up
```

The default endpoints are:

- Control Service: `http://localhost:8080`
- Model Gateway: `http://localhost:8090/v1`

Create the `enterprise-chat` model and assign it through `aepctl`, then log in
an Agent. Send the returned `modelAccessToken` as `Authorization: Bearer ...`
to `POST http://localhost:8090/v1/chat/completions` with
`{"model":"enterprise-chat",...}`.

The deterministic full scenario is:

```bash
npm run test:e2e:m1-gateway
```

It proves successful and streaming inference, Higress model rewriting, provider
credential injection without disclosure, and rejection of missing, invalid,
expired, or insufficiently scoped model tokens. The scenario removes its
containers and volumes on completion.

## Configuration

The development mapping lives in `deploy/compose/higress/ai-proxy.yaml` and
maps `enterprise-chat` to `mock-upstream-chat`. The mock provider key is test
data only. Real provider credentials must be supplied through the deployment's
secret mechanism and rendered into the Higress AI Proxy configuration without
entering the Agent-facing model descriptor.

Relevant authorizer environment variables are:

| Variable | Default | Purpose |
| --- | --- | --- |
| `AEP_GATEWAY_ADDRESS` | `:8090` | Listener address |
| `AEP_GATEWAY_UPSTREAM_URL` | `http://localhost:8080` | Internal Higress URL |
| `AEP_GATEWAY_JWKS_URL` | Control Service JWKS | Signing key source |
| `AEP_GATEWAY_ISSUER` | `http://localhost:8080` | Required token issuer |
| `AEP_GATEWAY_JWKS_TTL` | `5m` | Public-key cache TTL |
| `AEP_GATEWAY_REQUEST_LIMIT` | `2097152` | Maximum inference body bytes |

## Production Deployment

`higress-standalone` explicitly targets local deployment and testing. Production
must deploy Higress 2.2.4 with its Helm chart, place the authorizer before the
Higress service, expose only that authenticated entry point, and use TLS.
Production automation must render model routes and AI Proxy mappings from the
approved model catalog and provider-secret store. The static Compose mapping is
an M1 development fixture, not a catalog-to-Higress reconciliation controller.

Higress and higress-standalone are Apache-2.0 licensed and may be used
commercially. The M1 profile pins the official all-in-one image by digest for
repeatable local and CI verification.

# AEP M1 Node Agent Model Runbook

M1 connects the example Node Agent to the Higress OpenAI-compatible model data plane without moving inference into SDK Core. The Agent gets the gateway URL and short-lived model token from `AepClient.getModelConnection()`, then uses the Apache-2.0 licensed [official OpenAI Node SDK](https://developers.openai.com/api/docs/libraries).

## Client Flow

```text
Agent -> AEP login/model discovery -> SQLite token store
      -> OpenAI client with AEP model JWT -> authorizer -> Higress -> provider
      -> model telemetry -> SQLite outbox -> AEP batch upload
```

`AEP_MODEL_ID` selects an explicit visible model; otherwise the default model is used. Regular and streaming Chat Completions are supported. Telemetry records model ID, streaming mode, duration, safe HTTP status, and available token counts. It never records prompts, model tokens, gateway URLs, provider credentials, or provider error messages.

## Automated Demo

Prerequisites: Node.js 24, Go 1.26, Docker Desktop, and Docker Compose.

```bash
npm ci
npm run test:e2e:m1-client
```

The test builds SDK Core and the Agent, starts the complete M1 stack, provisions an assigned model, and launches the built Agent as a real child process. It verifies regular and streaming inference, failure telemetry, provider-secret isolation, outbox upload, and admin queries. Containers and volumes are removed afterward.

Full regression:

```bash
npm run check
go test ./...
go vet ./...
go build ./...
npm run test:e2e
```

## Manual Agent Command

Start and provision the stack using the [Higress gateway runbook](m1-gateway-runbook.md). With an account and `enterprise-chat` assignment ready, run:

```powershell
$env:AEP_BASE_URL = 'http://localhost:8080'
$env:AEP_ENTERPRISE_ID = 'demo'
$env:AEP_USERNAME = 'client-user'
$env:AEP_PASSWORD = 'temporary-password-123'
$env:AEP_AGENT_ID = 'demo-node-agent'
$env:AEP_AGENT_DATA_DIR = '.aep-agent-demo'
$env:AEP_CHAT_PROMPT = 'Hello from the managed Agent'
npm run start --workspace @aep/example-node-agent -- chat
```

Expected output:

```json
{"modelId":"enterprise-chat","responseModel":"mock-upstream-chat","content":"Hello AEP","streamed":false}
```

For streaming, set `AEP_MODEL_ID=enterprise-chat` and `AEP_CHAT_STREAM=true`. `AEP_MODEL_TIMEOUT_MS` defaults to `120000`. Query telemetry with `aepctl audit --agent-id demo-node-agent`.

## Boundary

`@aep/sdk-node` remains the AEP control/session SDK. Inference stays in the Agent. Provider secrets remain in Higress; the Agent receives only an expiring AEP model JWT scoped to assigned logical model IDs.

# Agent Enterprise Protocol

[简体中文](README.zh-CN.md) | English

Agent Enterprise Protocol (AEP) defines the interaction contract between an
enterprise-managed Agent client and enterprise services.

AEP v1 covers:

- user identity and session exchange;
- ZhiYuan platform password login and customer federated-login adapters;
- Agent telemetry event reporting;
- heartbeat-based discovery and reliable delivery of scoped control events;
- managed Skill discovery, download, update, removal, and sync reporting;
- client-deliverable API credential assignment and retrieval;
- model catalog discovery and model access control.

AEP is a management protocol. It does not redefine model inference, MCP, or
Agent-to-Agent protocols. Model calls use the protocol declared by the model
descriptor, such as an OpenAI-compatible API, while MCP connections continue
to use MCP.

## Documentation

- [AEP v1 protocol](docs/aep-v1.md)
- [AEP v1 API guide](docs/api-v1.md)
- [AEP M0 runbook and release checklist](docs/m0-runbook.md)
- [AEP M1 Higress gateway runbook](docs/m1-gateway-runbook.md)
- [AEP M1 Node Agent model runbook](docs/m1-client-runbook.md)
- [AEP M2 Credential runbook](docs/m2-credential-runbook.md)
- [AEP production runtime baseline](docs/production-runtime.md)
- [OpenAPI 3.1 specification](openapi/aep-v1.openapi.yaml)
- [M2 Credential OpenAPI profile](openapi/aep-v1-m2.openapi.yaml)
- [Control Events OpenAPI 3.1 specification](openapi/aep-v1-control-events.openapi.yaml)
- [Authentication OpenAPI 3.1 specification](openapi/aep-v1-authentication.openapi.yaml)

## Status

AEP v1 is an initial draft intended for implementation review. Breaking
changes may occur until the specification is marked stable.

## License

Apache License 2.0. See [LICENSE](LICENSE).

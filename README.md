# Agent Enterprise Protocol

[简体中文](README.zh-CN.md) | English

Agent Enterprise Protocol (AEP) defines the interaction contract between an
enterprise-managed Agent client and enterprise services.

AEP v1 covers:

- user identity and session exchange;
- Agent event reporting;
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
- [OpenAPI 3.1 specification](openapi/aep-v1.openapi.yaml)

## Status

AEP v1 is an initial draft intended for implementation review. Breaking
changes may occur until the specification is marked stable.

## License

Apache License 2.0. See [LICENSE](LICENSE).

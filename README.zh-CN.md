# Agent Enterprise Protocol

简体中文 | [English](README.md)

Agent Enterprise Protocol（AEP，Agent 企业协议）定义企业托管的 Agent
客户端与企业服务之间的交互契约。

AEP v1 覆盖：

- 用户身份识别与会话交换；
- 知远平台账号密码登录与甲方联合登录适配；
- Agent 遥测事件上报；
- 通过心跳发现并可靠投递具有不同作用域的服务端管控事件；
- 托管 Skill 的发现、下载、更新、删除与同步结果上报；
- 可下发到客户端的 API 凭证授权与获取；
- 模型目录发现与模型访问权限控制。

AEP 是管理协议，不重新定义模型推理、MCP 或 Agent-to-Agent 协议。模型调用
使用模型描述中声明的协议，例如 OpenAI 兼容 API；MCP 连接继续使用 MCP。

## 文档

- [AEP v1 协议说明](docs/aep-v1.zh-CN.md)
- [AEP v1 API 指南](docs/api-v1.zh-CN.md)
- [AEP M0 运行手册与发布检查](docs/m0-runbook.zh-CN.md)
- [AEP M1 Higress 网关运行手册](docs/m1-gateway-runbook.zh-CN.md)
- [OpenAPI 3.1 规范](openapi/aep-v1.openapi.yaml)
- [管控事件 OpenAPI 3.1 规范](openapi/aep-v1-control-events.openapi.yaml)
- [认证 OpenAPI 3.1 规范](openapi/aep-v1-authentication.openapi.yaml)

## 状态

AEP v1 当前为供实现评审使用的初始草案。在规范标记为稳定之前，可能发生破坏性变更。

## 许可证

Apache License 2.0，详见 [LICENSE](LICENSE)。

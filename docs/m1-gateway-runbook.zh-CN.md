# AEP M1 Higress 网关运行手册

M1 在模型控制面之上增加 OpenAI 兼容的数据面。Higress 2.2.4 负责供应商路由、
模型名改写、流式转发和服务端供应商凭证注入；位于 Higress 前方的轻量 AEP
gateway-authorizer 在本地校验模型令牌及请求模型权限。

## 架构

```text
Agent -- 模型 JWT --> gateway-authorizer --> Higress AI Proxy --> 模型供应商
               JWKS 缓存 ^                        | 服务端 API Key
                         Control Service           v
```

只有 `gateway-authorizer` 对宿主机开放。Higress 和 mock 供应商仅在 Compose
网络内可见。authorizer 转发前删除客户端 `Authorization`，随后由 Higress 注入
供应商凭证，因此模型目录和推理响应都不会暴露供应商密钥。

authorizer 接受带 JSON `model` 字段的 `POST /v1/*` 推理请求。它用缓存的 JWKS
按 `kid` 校验 Ed25519 签名，并校验 `iss`、`aud=model-gateway`、`exp`、`iat`、
`token_use=model`、AEP 身份字段，以及请求模型是否属于 `model_scopes`。JWKS
刷新不是逐请求授权决策，数据面也不持有签名私钥。

Higress v2.2.4 原生 JWT 插件不能完整强制执行 AEP 专用 claim，也不能把数组
claim 与 OpenAI 请求体中的模型动态比较，因此需要这个职责单一的 authorizer。

## 本地演示

需要 Node.js 24、Go 1.26、Docker Desktop 和 Docker Compose。

```bash
npm ci
npm run build
npm run compose:gateway:up
```

默认地址：

- Control Service：`http://localhost:8080`
- Model Gateway：`http://localhost:8090/v1`

通过 `aepctl` 创建并授权 `enterprise-chat` 模型，Agent 登录后取得
`modelAccessToken`。调用 `POST http://localhost:8090/v1/chat/completions`，令牌放在
`Authorization: Bearer ...`，请求体模型填写 `enterprise-chat`。

可重复的完整验收命令是：

```bash
npm run test:e2e:m1-gateway
```

该场景验证普通和流式推理、Higress 模型改写、供应商凭证注入且不泄露，以及
缺失、无效、过期或模型权限不足的令牌均被拒绝。完成后会删除容器和数据卷。

## 配置

开发映射位于 `deploy/compose/higress/ai-proxy.yaml`，将 `enterprise-chat` 映射为
`mock-upstream-chat`。mock 密钥仅为测试数据。真实供应商凭证必须来自部署环境的
Secret 管理机制，并渲染进 Higress AI Proxy 配置，不能进入 Agent 可见的模型描述。

authorizer 的主要环境变量：

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| `AEP_GATEWAY_ADDRESS` | `:8090` | 监听地址 |
| `AEP_GATEWAY_UPSTREAM_URL` | `http://localhost:8080` | Higress 内部地址 |
| `AEP_GATEWAY_JWKS_URL` | Control Service JWKS | 签名公钥来源 |
| `AEP_GATEWAY_ISSUER` | `http://localhost:8080` | 令牌签发者约束 |
| `AEP_GATEWAY_JWKS_TTL` | `5m` | 公钥缓存时间 |
| `AEP_GATEWAY_REQUEST_LIMIT` | `2097152` | 推理请求体字节上限 |

## 生产部署

`higress-standalone` 官方明确用于本地部署和测试。生产环境必须使用 Higress
2.2.4 Helm Chart，把 authorizer 部署在 Higress Service 前，只暴露经过鉴权的
入口并启用 TLS。生产自动化还必须根据审核后的模型目录和供应商 Secret 渲染
模型路由及 AI Proxy 映射；Compose 中的静态映射只是 M1 开发夹具，不是模型目录
到 Higress 的持续同步控制器。

Higress 与 higress-standalone 均采用 Apache-2.0，可免费商用。M1 开发配置按摘要
固定官方 all-in-one 镜像，保证本地与 CI 验收可重复。

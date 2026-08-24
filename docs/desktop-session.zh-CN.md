# 桌面端主进程会话接入

桌面应用必须在可信主进程中创建 `AepClient`。Renderer 可以通过窄 IPC 命令请求登录、退出和 AEP 操作，但不得获取密码、access token、model token、refresh token 或解析后的 Credential 明文。

`ProtectedRefreshTokenStore` 只在进程内存中保存完整 Token，并仅把 refresh token 交给注入的 `AepProtectedStorage`。进程重启后，`restoreSession()` 使用受保护的 refresh token 换取新的短期 Token；并发恢复和认证请求继续复用 SDK 的 refresh single-flight。

`agentId` 不是秘密，但必须只生成一次并保存在主进程应用配置中。refresh token 会绑定 Agent ID，每次启动生成新 ID 会导致会话无法恢复。

## Electron safeStorage 适配器

可直接采用英文文档中的 [Electron safeStorage 适配器](desktop-session.md#electron-safestorage-adapter)。该适配器只在 `app.getPath('userData')` 下保存 `safeStorage` 密文，使用临时文件加 rename 替换，并拒绝 Linux 的 `basic_text` 降级后端。

主进程的基本接入顺序如下：

```ts
const tokenStore = new ProtectedRefreshTokenStore(new ElectronSafeStorage());
const client = new AepClient({
  baseUrl,
  agentId: await loadStableAgentId(),
  agentVersion: app.getVersion(),
  platform,
  tokenStore,
});

const state = await client.getSessionState();
if (state.status === 'recoverable') await client.restoreSession();
```

`AepProtectedStorage.write` 必须在 Promise 返回前复制并保护传入字节，随后 SDK 会立即清零该字节数组。Electron 的 API 需要 JavaScript 字符串，该不可变字符串可能保留到垃圾回收，因此适配器只能存在于主进程，不得记录日志或通过 IPC 暴露。

退出时，`AepClient.logout()` 会在必要时先恢复冷会话、撤销服务端 refresh session，再删除本地受保护状态。如果恢复或远端撤销失败，本地状态仍会被删除，同时错误会继续抛出，便于产品提示“远端撤销未确认”。

Renderer IPC 只应返回登录状态和业务结果，例如 `signed-out`、`recoverable`、`authenticated` 及 `passwordChangeRequired`。密码只在一次登录调用期间进入主进程，不能持久化；Token 和 Credential 明文永远不应成为 IPC 返回值。

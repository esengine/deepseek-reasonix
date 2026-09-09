# 社区插件

Reasonix 会将外部工具作为 **MCP 服务器**加载。常规发现入口是官方 MCP
Registry：可在 **设置 → MCP 服务器 → 浏览 Registry** 中查找，也可运行
`reasonix mcp browse [query]`。安装和运行机制详见[指南](./GUIDE.zh-CN.md#插件mcp)。

本页作为 Registry 的补充，收录提供 Reasonix 专用接入说明的社区服务器。
收录项只是外部链接，不会打包进 Reasonix，也不代表官方背书或质量保证。

> 安装或声明 MCP 服务器就是一次信任决定。本地 stdio 服务器会作为子进程
> 运行；`readOnlyHint` / `destructiveHint` 是工作流元数据，不是针对恶意
> 代码的隔离边界。添加前请审查公开源码和已发布的软件包。

## 如何添加插件

每个 `[[plugins]]` 配置项都会声明服务器名称和启动方式。`type` 默认为
`stdio`；`command`、`args` 和 `env` 中的 `${VAR}` / `${VAR:-default}` 会被
展开。请固定软件包版本，避免已审查的配置在无感知的情况下运行其他
版本。`-y` 可避免 `npx` 首次安装确认阻塞 MCP stdio 握手。

```toml
[[plugins]]
name    = "example"
command = "npx"
args    = ["-y", "some-reasonix-plugin@1.2.3"]
# env   = { SOME_TOKEN = "${SOME_TOKEN}" }
```

启动后，工具会以 `mcp__<name>__<tool>` 的名称出现在会话中。可在聊天 TUI
中运行 `/mcp` 查看已连接服务器及其工具数量。

## 社区插件

暂无符合收录要求的条目。每个收录项都必须提供可访问的公开源码仓库和已发布的
版本化产物，让用户能够审查文档命令实际会执行的内容。

## 发布自己的插件

1. 使用任意语言构建支持 stdio JSON-RPC 的 MCP 服务器；可参考可运行的
   `cmd/reasonix-plugin-example`。
2. 发布源码、许可证、测试以及版本化安装产物。
3. 为工具声明真实的 annotations；只有行为确实只读的工具才能声明
   `readOnlyHint`。
4. 文档必须提供完整、可复制且固定版本的 `[[plugins]]` 配置。
5. 提交 PR，同时更新本页和 `PLUGINS.md`，并按插件名字母顺序排列。

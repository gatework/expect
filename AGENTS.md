# expect 开发说明

遵循 [gatework Go 工程规范（go-standards/STANDARD.md）](https://github.com/gatework/go-standards/blob/v1.2.0/STANDARD.md)；提交前运行 `make check`。

expect 是网络设备自动交互的流引擎：会话、匹配、应答、录像与脱敏。它是 net-connector 的底层依赖，取代冻结的 `../go-expect`（只作行为参考，不复制代码）。设计见 [docs/design.md](docs/design.md)，行为契约见 [docs/contracts.md](docs/contracts.md)，重写决策见 [docs/adr/0001-expect-rewrite.md](docs/adr/0001-expect-rewrite.md)，术语见 [GLOSSARY.md](GLOSSARY.md)。

成熟度：v0，未投产，不承诺兼容（GEN-1）。

## 边界

- 分层由 `architecture.yaml` 强制：`internal/scan`、`internal/redact`、`record` → 根包 → `spawn`、`expecttest`。新增包先登记。
- 只移动与匹配字节。厂商提示符、登录流程、命令成败判定属于 net-connector；本模块不导入 net-connector、netops 或 go-expect。
- 匹配状态只属于持有接收租约（`lease`）的一方；Expect、Poll、Interact、Background、Group、Relay 共用同一租约与 `waiter` 引擎，不另写接收循环。

## 不可破坏的语义

- `NewSession` 只在成功时接管传输；选项在任何 I/O 前校验。`Close` 幂等，打断读写与等待，不等待用户回调。
- 写入一旦进入传输即不重放；之后的任何失败关闭会话并返回带 `Written` 的 `*WriteError`。
- 溢出不丢数据：超出缓冲上限返回 `ErrBufferLimit` 并保留输入，除非显式 `FullBuffer` 或 `WithStreamingFallback`。
- `Result` 保留原始字节；录像、交互输出与 Relay 输出一律经脱敏过滤器。错误与事件只含元数据，不含设备输出或凭据（ERR-5）。
- **会话录像默认开启**（`~/.local/share/gatework/expect`），这是对 LOG-1“库默认不输出”的有意例外：运维回溯要求每次设备交互都有记录。关闭方式：`WithoutRecording()` 或 `EXPECT_RECORD=off`。测试与示例必须显式关闭或指向 `t.TempDir()`，不得写入开发者家目录。
- 录像失败默认只告警并丢弃该文件；`Required` 时会话失败。

## 构建与检查

```sh
make check            # 标准门禁 + cross、fuzz、bench、example
go test -race ./...
```

脚本化对端与本地 PTY 测试只证明会话逻辑，不代表真实设备兼容；Windows ConPTY 只有交叉编译证据。发布、打 tag 需明确授权（GIT-3）。

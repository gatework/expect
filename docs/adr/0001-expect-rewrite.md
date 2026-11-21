# ADR 0001：以 expect 重新实现 go-expect

日期：2026-11-20　状态：已接受

## 背景

`go-expect`（约 14k 行，源自 Netflix 的 Tcl-expect 移植）是 net-connector 的流引擎。它功能完整，但：每类接收方（Expect、Interact、Background、Group、Relay）各自维护预约与匹配循环；诊断分散在 Observer、Trace、Output、SendOutput 四套机制；没有会话录像，运维无法回溯一次设备交互；用例没有名字，日志里只能看到下标。

## 决策

新建模块 `github.com/gatework/expect`，按 go-expect 的行为契约重新设计实现，不复制代码。

1. **一个接收租约 + 一个等待引擎。** `lease` 是唯一的接收槽，`waiter` 是唯一的匹配循环；Group 是多成员的 waiter，Background 是在独立 goroutine 上可暂停的 waiter，Interact 与 Relay 复用其成员匹配与事件处理。
2. **诊断合并为两条通道。** 元数据事件 → 注入的 `*slog.Logger` 与 jsonl 录像（`slog.NewMultiHandler`）及可选 `Observer`；字节流 → `record.Transcript`。两者经同一脱敏过滤器。
3. **会话录像默认开启**，默认目录 `~/.local/expect`，`EXPECT_RECORD_*` 环境变量覆盖默认，显式参数覆盖环境。这是对 STANDARD LOG-1 的有意例外，理由是运维审计与故障回溯。
4. **用例有名字**（`Case.Name`），出现在 Result、事件与录像中。
5. **运维增补**：`RedactPattern`（配置密文行级脱敏）、`WithSearchWindow`（大输出下的线性提示符检测）、`ReplyLine`、`WithLabel`、`WithWindow`、`Session.Transcript()`。

## 依赖（GEN-4）

| 依赖 | 用途 | 标准库为何不够 |
|---|---|---|
| `github.com/caarlos0/env/v11` | 环境变量 → `record.Config` | 结构体标签、默认值、列表与 Duration 解析，及可注入环境 map 的测试入口 |
| `github.com/creack/pty` | Unix PTY | 标准库不提供 PTY 分配 |
| `golang.org/x/term`、`golang.org/x/sys` | 原始模式、termios、ConPTY、Job | 平台系统调用 |
| `golang.org/x/sync/errgroup` | Relay 路由的取消与汇合 | 标准库无带取消的 goroutine 组 |

考察后**未采用** `gopkg.in/natefinch/lumberjack.v2`：v2.2.1 每个 Logger 启动一个永不退出的 `millRun` goroutine（goleak 复现），按会话建文件会线性泄漏。改为每会话单文件 + `MaxSizeMB` 截断标记 + 按天目录 `MaxAge` 清理。

## 有意的差异

- 不移植 `WithOSPolling`、`SendAllPoll`：Go 运行时 netpoller 已提供就绪通知；`Poll` 改为只匹配已到达输入、不等待。
- `SendAll` 不移植：读泵始终在读，256 KiB 预读 + 缓冲上限保证对端先写后读时不死锁。
- Relay 目标同步写入（经各自串行 Send），去掉每目标队列与 `QueueLimit`；慢目标只拖慢本路由。
- Relay 与 Interact 对可能的部分匹配做保留（字面量精确保留前缀；正则保留当前行），静默 50 ms（Relay）或输入空闲（Interact）后放行。
- Console 移到 `spawn.Console`；根包不依赖 PTY。
- `SessionID` 改为随机十六进制字符串（文件名唯一，且无包级可变计数器）。

## 后果

- net-connector `terminal` 改为导入本模块；API 基本一一对应（`Case`、`Result.Captures`、`EOF`、`WithTimeout`、`WithIdleTimeout`、错误哨兵、`WriteError`）。
- 依赖本模块的测试需显式 `WithoutRecording()`，否则写入家目录。
- go-expect 冻结，仅作参考。

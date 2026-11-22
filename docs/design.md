# expect 设计

## 目标

把“与一台设备对话”的字节层做对：等到该等的话、答该答的问、不丢数据、不重放写入，并且每次对话都留下可回溯、已脱敏的记录。厂商知识（提示符、登录、成败判定）在 net-connector。

## 结构

```
           ┌──────────── 接收方 ────────────┐
 Expect  Poll  Background  Group  Interact  Relay
           └───────┬────────────────────────┘
                waiter（唯一匹配循环）
                   │ member × N（每成员：用例组、规则、扫描进度）
                lease（唯一接收槽，ErrBusy）
                   │
 transport ─► readLoop ─► inbox(256 KiB 预读) ─► rx(待处理输入, ≤ 缓冲上限)
     ▲            │
     │            └─► rxFilter(脱敏) ─► record.Transcript (log / cast / jsonl)
 Send* ─► txFilter(脱敏) ─► Transcript，然后 ─► transport（串行、截止时间、WriteError）
 事件 ─► slog.MultiHandler(注入 Logger, jsonl) + Observer
```

| 文件 | 职责 |
|---|---|
| `session.go` | 构造、Close、读泵、输入规范化、脱敏与录像 |
| `inbox.go`、`lease.go` | 读泵→接收方交接；接收槽与待处理输入 |
| `wait.go` | `waiter`/`member`/`entry`：分组优先级、最早字节、次数上限、事件用例、超时预算 |
| `expect.go`、`background.go`、`group.go`、`interact*.go`、`relay.go` | 各接收方 |
| `write.go` | 发送族、截止时间中断、退役 |
| `event.go` | 事件、错误分类 |
| `internal/scan` | 增量匹配：字面量与行局部正则断点续扫、Glob 翻译、All |
| `internal/redact` | 流式脱敏：保留可能的密钥前缀；跨块、重叠、相邻合并；行级规则 |
| `record` | 录像配置（参数 > 环境 > 默认）、文件布局、三种格式、按天清理 |

## 匹配

每次扫描按组：Before → 用例 → WithRules → After。第一个有匹配的组胜出；组内取最早开始的匹配，声明顺序破平（`WithMatchOrder(Declaration)` 取首个声明）。匹配消费到其结尾后运行处理器；`Continue` 重置相对计时器，`ContinueKeepTimer` 不重置。二者对应 Tcl 的 `exp_continue` 与 `exp_continue -continue_timer`；便捷包装 `Then`/`ReplyLineThenContinue` 用前者，`ThenKeepTimer` 用后者。

`Exchange(ctx, line, cases)` = 先取租约、再 `SendLine`、再等待，计时器从发送后开始；总是开启 `WithOutput`：`Result.Output` 按顺序收集本次等待消费的全部字节（含继续的应答与分页），流式回退交出的字节除外；返回用例的 `Before`/`Match` 是 `Output` 的子串，大输出只复制一次。`WithOutput` 也可用于 Expect、Poll、Background、Group。取租约失败（`ErrBusy`）时不发送任何内容。逐接口“先初始化再配置”用会话 Before 规则应答 `[Y/N]`，每个接口一次 `Exchange`。

增量：缓冲只追加时，字面量从上次末尾回退 `len-1` 处续扫；可证明不跨行、不锚定文本开头的正则从最后一行行首续扫；其余正则全量扫描，可用 `WithSearchWindow(n)` 限定尾部窗口。所有正则（含 Glob 与忽略大小写字面量）先做必需字面量预筛：顶层串接上每个匹配都包含的最长字面量（忽略大小写时只取折叠集全在 ASCII 内的字符，跳过 U+FFFD），增量查找；不在缓冲中则不运行正则。任何消费或规则切换都重置进度。

不完整的尾部 UTF-8 在更多字节或 EOF 到来前不参与正则搜索。

## 录像

`<Dir>/<YYYY-MM-DD>/<label>_<HHMMSS>_<session>.{log,cast,jsonl}`，目录 0700、文件 0600。新建 Recorder 时删除超过 `MaxAge` 的日期目录（只认日期名）。单文件到 `MaxSizeMB` 写截断标记后停止。每个文件经 64 KiB 缓冲写入：缓冲满、首个未落盘写入后 1 秒（定时器，不常驻协程）、`Close` 时落盘；进程崩溃最多丢失约 1 秒录像。`Required=false` 时写入失败只告警并丢弃该文件。

| 变量 | 默认 |
|---|---|
| `EXPECT_RECORD` | `on` |
| `EXPECT_RECORD_DIR` | `~/.local/share/gatework/expect` |
| `EXPECT_RECORD_FORMATS` | `log,cast` |
| `EXPECT_RECORD_MAX_SIZE` | `100`（MB） |
| `EXPECT_RECORD_MAX_AGE` | `720h` |
| `EXPECT_RECORD_REQUIRED` | `off` |

## 运维用法

- 收集大输出：`Exchange` 的 `Result.Output` 已含分页前后的全部文本；或 `WithStreamingFallback` 把未匹配输出分块交出，配合 `WithSearchWindow(4<<10)` 保持提示符检测线性。
- 设备卡住：`WithIdleTimeout` 区分“慢”和“停”。
- 静默设备：`Timeout()` 用例发送回车并 `Continue`（处理器获得新预算）。
- 配置密文：`RedactPattern(regexp.MustCompile(`cipher (\S+)`))` 只遮蔽子匹配。
- 人工接管：`InteractTerminal(ctx, os.Stdin, os.Stdout)`，Control-] 退出；输入用例可做宏。
- HA 双机/多控制台：`Group.Expect` 轮询公平，`Case.Sessions` 绑定成员。

# 行为契约

每条契约都有测试；括号内为测试名。

## 所有权与生命周期

- `NewSession` 校验全部选项（含 nil、typed nil 传输）后才接管传输；失败不接管（`TestNewSession_Validates`）。
- 读泵是传输的唯一读者。接收方互斥，冲突返回 `ErrBusy`（`TestSession_Busy`、`TestBackground_PauseResume`）。
- `Close` 幂等、返回同一错误，打断进行中的等待（`ErrClosed`）与写入，汇合读泵，刷新脱敏尾部与录像（`TestSession_CloseInterruptsExpect`）。不等待用户回调。
- 测试无 goroutine 泄漏（各包 `TestMain` 中的 goleak）。
- `Done`/`Err`：输入结束、`Close`、写失败作废三者先到者为准，不发起等待也能感知断开，待处理输入仍可匹配（`TestSession_DoneOnPeerClose`、`TestSession_ErrAfterCloseAndRetire`）；`Stats` 计数与空闲时长（`TestSession_Stats`）。
- 保活只在空闲且无接收方时发送，只丢弃对保活的应答，期间到来的接收方等待而非 `ErrBusy`（`TestKeepalive`、`TestKeepalive_ReceiverWaits`）。
- 传输在 `Close` 后 5 秒仍未打断 Read 时输出 Warn（`TestSession_CloseWarnsStuckTransport`）；会话打开、关闭各一条 Info 汇总（`TestSession_LifecycleLogs`）；后台处理器 panic 记录堆栈而非值（`TestBackground_PanicLogsStack`）。

## 匹配

- 组优先级与最早字节、声明顺序（`TestSession_Priority`）。
- 超时保留待处理输入，下一次等待可见（`TestSession_TimeoutKeepsInput`）；`Timeout` 用例获得新预算并可继续（`TestSession_TimeoutCase`）。
- 空闲超时（`TestSession_IdleTimeout`）。
- EOF：无用例返回 `io.EOF` 并保留尾部；`EOF` 用例消费尾部（`TestSession_EOF`）。由 EOF 与其他错误合并的错误不是干净 EOF。
- 缓冲上限：`ErrBufferLimit` 不丢数据；`FullBuffer` 显式消费（`TestSession_BufferLimit`）；流式回退分块交出且总和完整（`TestWithStreamingFallback`）。
- 次数上限在消费前检查（`TestSession_CaseLimit`）；`NoConsume` 同一候选只观察一次，后续数据到达也不重复触发（`TestSession_NoConsume`、`TestSession_NoConsumeAcrossChunks`）。
- `Settle` 提示符只在最后一行匹配，静默满时长才触发，输入结束立即确认，Poll 不等待，Group 共用计时（`TestSettle_PromptInOutput`、`TestSettle_EndOfInputConfirms`、`TestSettle_Group`）。
- `WithOutput` 按序收集继续与返回的全部消费字节，`Omit` 排除分页与提示符，`WithOutputLimit` 越界即失败（`TestWithOutput`、`TestCase_Omit`、`TestWithOutputLimit`）。
- `WithTerminalFilter` 剥离控制序列、退格与光标左移擦除未消费字符，任意分块结果一致；录像与 Interact 保留原始字节（`FuzzFilterChunks`（internal/vt）、`TestTerminalFilter_HuaweiPager`、`TestTerminalFilter_InteractRaw`）。
- `WithCharset` 跨块解码，中文提示可匹配、可应答，录像为 UTF-8；无法编码时不发送（`FuzzCharsetChunks`、`TestCharset_GB18030`、`TestCharset_Unencodable`）。
- `SetRules` 在下一次扫描生效（`TestSession_SetRules`）。
- 任意分块与一次性到达结果一致（`FuzzSessionChunks`、`FuzzFindChunks`）。

## 写入

- 写前失败（关闭、上下文已结束、必需录像失败）会话仍可用；进入传输后的失败关闭会话，返回 `*WriteError{Written}`，不重放（`TestSession_WriteErrorRetires`）。
- 有写截止时间的传输经截止时间中断，否则关闭传输中断；清除已关闭传输的截止时间无害。
- `SendSlow`/`SendPaced`/`SendNull`/`SendBreak`（`TestSession_SendVariants`、`TestSession_SendBreak`、`TestSession_SendNull`、`TestSession_SendSlow`、`TestSession_SendPacedCancel`）；`Resize` 经 `Resizer` 改窗口并写 cast `r` 事件（`TestSession_Resize`）。
- `Exchange` 先取输入再发送，忙时不发送（`TestSession_ExchangeBusySendsNothing`）；`WithEcho` 丢弃发送前与回显前的陈旧输入，超时重试不被旧回显满足，无回显报 `ErrNoEcho`（`TestEcho_StaleOutput`、`TestEcho_RetryAfterTimeout`、`TestEcho_Missing`）；`WithEchoWait` 等不到回显时退化为延迟并继续匹配，仍丢弃发送前的输入（`TestEchoWait_Degrades`、`TestEchoWait_Synchronizes`）；`Drain` 静默后清空（`TestDrain`）。
- `Then` 重置计时器（Tcl `exp_continue`），`ThenKeepTimer` 不重置（`TestThen_RestartsTimer`）。

## 脱敏与录像

- 任意分块不泄露密钥，输出与分块无关（`FuzzFilterChunks`）；行级规则只遮蔽子匹配（`TestFilter_Rule`）。
- 录像中的密码与配置密文已遮蔽，jsonl 记录用例名与关闭事件（`TestSession_TranscriptRedacted`）。
- 交互输出与 Relay 输出跟随运行中新注册的密钥（`TestSession_InteractRedactLater`、`TestRelay_RedactLater`）。错误与格式化值不含负载（`TestErrors_NoPayload`）。
- 失败的等待输出 Info 诊断（用例、待处理字节、静默时长、超时、录像路径）；`WithMatchTrace` 的尾部片段经脱敏，被截断的密钥仍遮蔽（`TestWaitFailed_Diagnostics`、`TestMatchTrace_Scan`）。
- 录像回放：cast 解析、回放复现录制时的交互，密钥位置通配（`TestReplay`、`TestReplaySteps`、`TestReadCast_Rejects`）。
- 录像缓冲写入、1 秒内落盘，`Required` 时直写（`TestBufferedFile_FlushesLater`、`TestBufferedFile_Through`）。
- 默认开启并写入 `~/.local/share/gatework/expect`，`EXPECT_RECORD=off` 关闭（`TestNewSession_RecordsByDefault`）；配置解析、清理、权限、`Required`（`record` 包测试）。

## 接收方

- Background 暂停交还输入、恢复重新获取、暂停中可停止、回调 panic 报告为 `CallbackError`（`TestBackground_*`）。
- Group 成员绑定、运行中增删成员（`TestGroup_*`）。
- Interact 输出脱敏、输入宏、退出键关闭输入（`TestSession_Interact`、`TestSession_InteractOutputCase`）。
- Relay 扣留可能匹配的字节（含会话规则）、EOF/Return 结束、源不可重复（`TestRelay*`、`TestRelay_HoldsSessionRulePrefix`）。
- spawn：对话、退出码、取消挂断、窗口与回显、Console（`spawn` 包测试，Unix）。

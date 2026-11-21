# 行为契约

每条契约都有测试；括号内为测试名。

## 所有权与生命周期

- `NewSession` 校验全部选项（含 nil、typed nil 传输）后才接管传输；失败不接管（`TestNewSession_Validates`）。
- 读泵是传输的唯一读者。接收方互斥，冲突返回 `ErrBusy`（`TestSession_Busy`、`TestBackground_PauseResume`）。
- `Close` 幂等、返回同一错误，打断进行中的等待（`ErrClosed`）与写入，汇合读泵，刷新脱敏尾部与录像（`TestSession_CloseInterruptsExpect`）。不等待用户回调。
- 测试无 goroutine 泄漏（各包 `TestMain` 中的 goleak）。

## 匹配

- 组优先级与最早字节、声明顺序（`TestSession_Priority`）。
- 超时保留待处理输入，下一次等待可见（`TestSession_TimeoutKeepsInput`）；`Timeout` 用例获得新预算并可继续（`TestSession_TimeoutCase`）。
- 空闲超时（`TestSession_IdleTimeout`）。
- EOF：无用例返回 `io.EOF` 并保留尾部；`EOF` 用例消费尾部（`TestSession_EOF`）。由 EOF 与其他错误合并的错误不是干净 EOF。
- 缓冲上限：`ErrBufferLimit` 不丢数据；`FullBuffer` 显式消费（`TestSession_BufferLimit`）；流式回退分块交出且总和完整（`TestSession_StreamingFallback`）。
- 次数上限在消费前检查（`TestSession_CaseLimit`）；`NoConsume` 同一候选只观察一次（`TestSession_NoConsume`）。
- `SetRules` 在下一次扫描生效（`TestSession_SetRules`）。
- 任意分块与一次性到达结果一致（`FuzzSessionChunks`、`FuzzFindChunks`）。

## 写入

- 写前失败（关闭、上下文已结束、必需录像失败）会话仍可用；进入传输后的失败关闭会话，返回 `*WriteError{Written}`，不重放（`TestSession_WriteErrorRetires`）。
- 有写截止时间的传输经截止时间中断，否则关闭传输中断；清除已关闭传输的截止时间无害。
- `SendSlow`/`SendPaced`/`SendNull`/`SendBreak`（`TestSession_SendVariants`）。

## 脱敏与录像

- 任意分块不泄露密钥，输出与分块无关（`FuzzFilterChunks`）；行级规则只遮蔽子匹配（`TestFilter_Rule`）。
- 录像中的密码与配置密文已遮蔽，jsonl 记录用例名与关闭事件（`TestSession_TranscriptRedacted`）。
- 默认开启并写入 `~/.local/expect`，`EXPECT_RECORD=off` 关闭（`TestNewSession_RecordsByDefault`）；配置解析、清理、权限、`Required`（`record` 包测试）。

## 接收方

- Background 暂停交还输入、恢复重新获取、暂停中可停止、回调 panic 报告为 `CallbackError`（`TestBackground_*`）。
- Group 成员绑定、运行中增删成员（`TestGroup_*`）。
- Interact 输出脱敏、输入宏、退出键关闭输入（`TestSession_Interact`、`TestSession_InteractOutputCase`）。
- Relay 扣留匹配字节、EOF/Return 结束、源不可重复（`TestRelay*`）。
- spawn：对话、退出码、取消挂断、窗口与回显、Console（`spawn` 包测试，Unix）。

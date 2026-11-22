# 术语

| 术语 | 含义 |
|---|---|
| 会话（Session） | 与一个对端（设备、进程）的一次对话，独占一个字节流传输。 |
| 用例（Case） | 一个模式 + 可选处理器（应答并继续或返回），带名称与次数上限。 |
| 模式（Pattern） | 文本模式（Literal、Line、正则、Glob、All）或事件（EOF、Timeout、Default、FullBuffer）。 |
| 规则（Rule） | 每次等待都生效的可复用用例：会话级 Before/After，单次等待的 WithRules；用于分页、横幅、异步日志。 |
| 等待（Wait） | 一次 Expect/Poll/Background/Group/Relay：按组优先级匹配直到某用例返回或失败。 |
| 交换（Exchange） | 一次“发一行 + 等待”：先取租约再发送，`Result.Output` 汇集等待中继续与返回的全部文本。 |
| 省略（Omit） | 用例匹配文本不计入 `Result.Output`，用于分页提示与提示符。 |
| 继续（Continue） | 处理器应答后让等待继续，等同 Tcl `exp_continue`；`ContinueKeepTimer` 不重置计时器。 |
| 接收租约（Lease） | 消费会话输入的唯一权利；第二个接收方得到 `ErrBusy`。 |
| 待处理输入（Pending input） | 已收到尚未被匹配消费的字节；失败时保留给下一次等待。 |
| 录像（Transcript） | 一次会话的回溯文件：`log`（可 grep）、`cast`（asciinema 回放）、`jsonl`（事件）。 |
| 脱敏（Redaction） | 在录像与交互输出中遮蔽凭据（字面量）与配置密文（行级正则子匹配）。 |
| 接管（Interact） | 把会话交给值班人员：设备输出给人看，人的输入发给设备，退出键或用例结束。 |
| 标签（Label） | 对端名称（设备地址或主机名），出现在日志属性与录像文件名中。 |

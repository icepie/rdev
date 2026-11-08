# 子 Agent RPC 帧核对

参考 OMP 75e7801 的 modes/rpc/wire/commands.ts、wire/state.ts 和 rpc-mode.ts：命令名直接放在 type；回执为 response，结果在 data。纠正此前 ebe08b9/fb2ff87 的 type=request 包装和 items 结果字段说明。

agent_subagent_rpc_test.go 通过真实已认证 WebSocket 发送原生 JSON 帧，直接检查 JSON 字段，避免 Go agentMsg 解码掩盖字段错误：

- get_subagents 必需摘要字段、空数组及 shell 排除。
- steer_subagent 成功进入子任务指导队列、跨线程和 shell 拒绝。
- cancel_subagent 首次 true、重复和未知任务 false、data 字段而非 items、停止后的指导拒绝。
- 请求 ID、command、显式 success:false 和错误消息。
- 取消不影响同线程 shell。

全量 go test ./... 通过；包含新增原生帧测试的定向 go test -race 通过。未验证完整 OMP 客户端启动/订阅/事件序列；实现边界见 agent-workspace.md 的子 Agent 帧适配章节。

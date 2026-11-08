# Compact RPC 验证

原生 compact 命令复用已有前台压缩生命周期，移除 admission 时的 accepted 回执。异步请求绑定 turn 快照及原连接，成功/失败/重置后各返回一次最终响应；成功摘要在排队的下一轮启动前捕获，避免后续历史写入改变结果。

`TestAgentRPCCompactLifecycle` 使用受控 SSE 提供方和两个真实 WebSocket，覆盖：

- customInstructions 传递、非法类型、混合 op、未知线程。
- 提供方完成前不确认成功，忙碌线程拒绝第二次压缩。
- 成功返回实际摘要且磁盘历史已替换。
- 提供方无有效摘要、reset 取消均返回失败。
- 双连接顺序屏障验证没有重复回执或向旁观者广播。
- 既有认证回归增加 compact 帧。

验证命令：`go test ./internal/server -run 'TestAgentRPC|TestCompact' -count=1` 与相同范围的 `-race` 均通过。

范围边界：不伪造 OMP entry ID 或压缩前 token 估算；结果只提供实际 summary，仍不宣称完整 OMP RPC 兼容。此次未改变 Web UI。

# Thinking level RPC 与下一轮生效验证

对照 OMP `rpc-mode.ts` 的 `set_thinking_level`：接收 `level`，成功返回关联 `response`，没有额外结果数据。rdev 当前接受 off/low/medium/high，沿用浏览器的设备级偏好；明确拒绝非空 thread，未实现线程独立覆盖，也不宣称支持 OMP 所有思考级别。

修复原有延后生效缺口：运行中的线程跳过立即重建后，过去没有任何待应用标记，下一轮可能继续使用旧级别。现在标记该线程，在统一 startAgentTurnLocked 边界重建，保留历史与模型。重置、模型切换等已经建立新会话的路径清除此标记。当前模型响应及其工具批次继续使用原会话。

证据：

- TestReasoningChangeAppliesAtNextTurn：受控 SSE 提供方记录请求，第一轮 medium，运行中设置 low 后再设 high；当前 Session 不变，下一轮请求 high，且包含上一轮回答。分别覆盖手动下一轮、队列自动启动，并核对保存的偏好。
- TestSubagentRPCWireContract：真实 WebSocket 的成功回执、无额外结果数据、非法类型/缺字段/无效级别/线程参数拒绝，以及失败请求不改变已保存设置。
- TestAgentRPCRequiresAuthentication：未认证的思考级别设置不调度。
- 上述测试及 TestTurnSnapshotSurvivesSessionReplacement 的 race 检查通过。

此次只改变服务端，不改变 UI 的 token 显示或主题。

# Todo RPC 输入与回读验证

修复 set_todos 接受 phases:null 并误清空原列表的问题：phases 必须为非 null 数组，每个 phase 的 tasks 必须为数组。清空使用 phases:[]。补齐 OMP TodoItem.notes，避免回执和 get_state 丢弃备注。

TestRPCTodosRoundTripAndInvalidInput 通过真实 WebSocket 验证：

- blocked 任务的 phase、blocker、details 和 notes 在 get_state 回读保持一致。
- null、非数组、缺失/null tasks、未知状态、错误 notes 类型与未知线程返回失败，逐次回读确认原始列表未改变。
- 空数组明确清空 RPC phases 与浏览器 todos。

该测试与 TestSubagentRPCWireContract 的 race 检查通过。此记录不证明待办的重启持久化、全部 OMP 类型验证或完整 UI blocked/abandoned 展示；这些仍需独立验证。

## 待办卡状态展示补充

对照 `/tmp/rdev-bb-reference/apps/app/src/components/promptbox/banner/ThreadTodoCard.tsx`：活动任务使用静态方框，完成计数仅统计 completed。rdev 补充 OMP blocked/abandoned 的语义显示，沿用 BB 主题的 surface-attention/warning-text，不计放弃任务为完成；状态文字提供中英文，折叠触发器关联唯一列表 ID，阻塞原因从服务端扁平待办投影传递至卡片。

`web/tests/todo-card.test.js` 验证五种状态的排序、1/5 完成统计、阻塞原因、放弃标签和折叠关联；Web 264 测试、typecheck、production build 通过。服务端回读测试增加 blocker/details/notes 的浏览器投影检查。此轮未做浏览器像素截图验收，不以 DOM 测试声称亮暗色逐像素一致。

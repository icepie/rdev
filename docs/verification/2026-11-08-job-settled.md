# 后台结果递交与 settled 状态

修复 get_state 只扫描 running jobs 的间隙：worker 在 jobs 锁内设置终态，结果回调却在锁外递交。其间客户端可能看到前台 idle、队列空、jobs 全部 completed，错误认为不再有后续工作。

现在 jobs 管理器在发布终态的同一锁区登记 pendingDeliveries，回调返回时递减；HasPendingWork 在同一锁内检查运行任务和待递交结果。get_state 在原有会话锁下查询它，无需拷贝并排序完整任务列表。回调仍不持有 jobs 锁，保持既有锁序。

TestRPCStateWaitsForBackgroundDelivery 使用受控回调和真实 WebSocket：Read 已报告 completed，但 get_state 仍 pending、非 settled；放行回调并确认返回后，才报告无待处理工作。该测试及后台任务、慢订阅者相关 race 测试通过。

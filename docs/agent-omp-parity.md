# OMP → Go 同步记录

本轮对照上游 **OMP 18.18.3**，提交 `21f1188cf88b2f534c092181968cd5a4d1a9c296`（2026-11-09 拉取 `origin/main` 验证）。rdev 保留 Go 服务端和远程设备工具路由，不引入 Bun 运行时，也不将两个不同项目强行 git merge。上游 MIT 许可保存在 `third_party/omp/LICENSE`。

## 本轮移植

| 上游逻辑 | Go 实现和验证 |
| --- | --- |
| `packages/agent/src/agent-loop.ts` 持续处理工具/steering，直到模型完成或取消 | 默认轮数上限改为 0（不限固定轮数）；每轮检查取消；完成回答和工具边界仍消费 steering。测试连续 30 次工具调用后正常结束、显式上限、取消。 |
| `openai-completions.ts` 的完成原因映射 | 大小写兼容；`length` 仍是截断；`content_filter` 是不可重试的供应商中断；未知原因保持错误；资源不足、网络、网关 error 等标记为临时失败。 |
| `native-finish-reason.ts` | 识别 `MALFORMED_RESPONSE`、`MALFORMED_FUNCTION_CALL`、`UNEXPECTED_TOOL_CALL`、`NETWORK_ERROR`；锁存原生失败，后来的 `stop` 不会把它恢复为成功；不认识的新原生值不直接判错。 |
| `reasoning_content` / `reasoning` / `reasoning_text` | 按上游顺序选择第一个非空字段，避免重复思考内容；保留 refusal 文本。 |
| 完成标记后的 2500 ms 收尾窗口 | 继续接收尾部 usage；网关不关闭 SSE 时限时关闭读取，不把已经完成的回答误判成超时。 |
| 失败轮的会话边界 | 已显示的文字和思考保留，未执行的工具调用不进入可回放历史；过滤响应不重复发送，也不删掉用户请求。Responses API 的 `content_filter` 同样归类。 |

## rdev 的明确边界

- 当前支持的供应商 API 仍为 Chat Completions、Responses、Anthropic Messages；不是对 OMP 全部供应商、OAuth、TUI 或本地工具的移植。
- 默认无 25 轮硬限制。若显式设置 `--agent-max-turns N` 或 `RDEV_AGENT_MAX_TURNS=N`（N > 0），仍执行该上限。改回 0 可取消固定上限，手动停止始终有效。
- 重试保持 rdev 的单次、无可见输出边界，避免重复追加已显示内容或重放设备操作；仅移植错误分类，没有宣称移植 OMP 全部重采样/重试预算机制。401 等永久 HTTP 错误和内容过滤不重试。
- `/compact`、队列、后台任务、子任务及已有供应商回放逻辑保留 Go 实现；本轮不声称同步 OMP 全部自动压缩策略与会话存储格式。
- 内容过滤是供应商行为，无法在客户端“恢复”被拦截的内容。页面用明确的中断提示替代底层 `finish_reason` 报错，允许修改请求后继续。

对应回归测试：`turn_limit_test.go`、`provider_finish_test.go`、`stream_failure_test.go`、既有 steering/provider suites，以及 `web/tests/thread-sink.test.js`。

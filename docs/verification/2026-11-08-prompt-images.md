# Prompt RPC 图片链路

修复 prompt 忽略 images 字段的问题。与 steer/follow_up 共用 parseRPCImages 转换和图片限制，启动帧与实际会话均使用解析后的 images；允许有图时 message 为空，完全空输入仍拒绝。

TestRPCPromptImageReachesProvider 通过真实 WebSocket 发送生成的 PNG，分别覆盖图文和纯图片。检查 turn_start.imagePreviews、最终关联回执，以及受控 SSE 提供方收到的 image_url 中完整原始 base64 和 MIME。先发送空文本、null images、非法 base64 并确认 invalid_request，随后合法输入仍成功。相关专项 race 测试通过。

范围：该测试证明图片运输和预览，不证明真实视觉模型质量；prompt 仍使用目前 rdev 的最终 response 与浏览器 turn 事件，不等价于 OMP 完整 prompt_result/流式事件契约。

# Agent 工作区

## 会话与上下文

同一设备可以拥有多个独立会话；不同会话并发执行，同一会话的前台消息排队。浏览器断开不终止服务端执行，重新连接恢复历史与当前输出。

在输入框使用 `/compact` 压缩旧上下文，也可用 `/compact 保留错误和文件路径` 指定重点。压缩期间不向摘要模型提供工具；保留最新会话及完整工具调用/结果链。失败或取消不会替换原历史。上下文过短时返回无需压缩的错误。新建、重置、停止、模型切换等操作由输入区命令菜单和会话控件提供。

## 后台任务与子 Agent

模型可以调用以下工具，用户也可用自然语言要求创建任务：

- `bg`：在远程设备上运行后台 shell，立即返回任务 ID。任务默认运行至实际完成；可设置 0–86400 秒超时，0 表示不设期限。非零退出码记为失败。
- `jobs`：列出、读取、等待或取消本会话任务。单次等待最多 30 秒，等待超时不取消任务。
- `subagent`：创建独立模型上下文，委派明确任务和设备工具子集。默认使用可用的只读工具；使用父会话的模型/provider，最长运行 30 分钟。子 Agent 不递归创建后台任务或子 Agent。

`/jobs` 直接打开任务面板，`/jobs full` 展开完整任务名，`/jobs kill <id>` 取消单个任务，`/jobs kill all` 取消本会话当前任务。它们不经过模型。任务面板显示状态、输出和取消入口。任务完成后自动通知原会话；前台忙碌时排队，空闲时继续处理结果。每个会话最多同时运行 8 个任务，最多保留 64 个任务记录；达到保留上限时淘汰最早的已结束任务。每个任务的输出、结果及错误分别保留最后 32KiB，截断不切断 UTF-8 字符。取消请求发出后，执行结束才释放并发名额。

启动子 Agent 的授权包括所选能力在该任务内的使用，不依赖已经结束的父轮次审批。重置或删除会话会取消所属任务。任务状态目前只在服务端进程内保存，服务端重启不会恢复运行中的后台任务。

## 服务端协调模式

选择“服务端”（内部目标 `@server`），直接让 Agent 列出设备或指定设备执行任务，无需注册设备工具。`list_devices` 返回当前在线设备和工具参数；`device_tool` 接受明确的 device、tool、arguments，支持 shell、list_dir、read_file、write_file、edit、grep、glob。

入口位于 Agent 左侧栏底部的设备选择菜单，选择“服务端”；也可直接打开 `/agent?device=%40server`。设备菜单合并在线设备与历史会话，服务端不依赖已有会话，始终可选；仅有离线历史的设备保留显示。

服务端只负责协调，不执行宿主机 shell 或文件读写。后台 shell 同样需要明确指定远程设备；每次调用检查最新连接状态。管理令牌仍保护 Agent 入口；没有管理令牌时，服务端模式不能绕过远程设备的密码保护。

## 并发与持久化边界

审批和问题 ID 包含会话标识，多个会话的回复不会串线。历史保存、重置、删除和元数据更新按会话串行；偏好按设备串行保存，写盘不占用全局会话锁。存储实现仍串行处理 JSONL 写入。

Agent 的 write_file/edit 使用按设备与路径隔离、可取消的写入锁，防止同一文件读改写互相覆盖；不同文件和不同设备继续并行。Windows 路径统一分隔符和大小写。该锁不覆盖外部编辑器、shell 写入、符号链接或不同相对/绝对路径别名。

会话历史按 `<data>/agent/sessions/<device>/<thread>.jsonl` 保存；偏好保存在 `<data>/agent/prefs/`。关闭持久化时仍可以在进程内使用会话与后台任务。

## 会话管理

会话行的菜单提供重命名和删除，删除确认展示具体会话名称。删除会取消该会话的前台轮次、后台任务及队列，清除持久化历史；正在查看被删除会话的浏览器切换到最近的剩余会话，没有剩余会话时创建空会话。其他会话不受影响。跨设备管理不切换当前页面，管理离线或受密码保护设备的历史需要管理员认证。

### Slash shortcuts and execution

`/compact` uses server-side context compaction. `/jobs` and `/jobs kill <id|all>` use the thread-scoped job protocol. `/bg <command>` and `/subagent <task>` currently send explicit tool-use requests to the model; they are shortcuts, not deterministic server command execution. The model still selects and invokes the tool under the conversation's permission rules. Server mode still requires a remote device for shell work. These chat-based commands carry the selected model and thread, preserve multiline arguments, and keep the draft if sending fails.

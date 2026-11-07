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

`/compact` uses server-side context compaction. `/jobs` and `/jobs kill <id|all>` use the thread-scoped job protocol. `/bg <command>` and `/subagent <task>` deterministically invoke their tools through the conversation queue without asking the model to select them. They retain the same approval, cancellation, history, and thread isolation rules as model tool calls. In server mode use `/bg --device <id> <command>` to select the remote device. Server mode still requires a remote device for shell work. These chat-based commands carry the selected model and thread, preserve multiline arguments, and keep the draft if sending fails.

侧栏区分运行中、等待审批、等待回答，并显示运行中的后台任务数。当前页面观察到会话从运行转为空闲时，未查看的会话显示完成未读圆点；在可见页面打开该会话后清除。分屏内可见的会话视为已读，后台浏览器标签页不自动清除。未读标记在当前标签页内保存，刷新后恢复；具体边界见下方更新。

## 2026-11-08 交互与协议更新

- `turn_start`、终态和历史条目增加可选 `display` 元数据：服务端时间、Steer 来源、结构化任务通知。元数据持久化但不进入 provider 请求；旧文本通知仍兼容。队列中的后台通知显示任务摘要。
- 新会话轮次显示服务端计时的耗时，重连后保留；旧历史没有时间数据时不猜测。Steer 仍遵循 rdev 的“取消当前轮次并优先执行插入消息”语义，不宣称与 OMP 的运行中 steering 完全等价。
- 消息导航参考 BB `ThreadTableOfContents`，支持 Agent／用户消息索引、当前位置和会话内跳转；分屏按各自滚动容器定位。
- 未读使用标签页内的 `sessionStorage`，刷新保留未读及已观察到的运行状态；初次访问不把旧历史全部标为未读。已见过的会话通过持久化的 outcomeAt 标记识别断线期间完整执行的前台轮次，不依赖看到 running 状态；首次发现的历史不会自动标为未读。后台任务重启恢复不在此机制内。
- 发送前图片、发送后的缩略图和 `read_file` 图片共用放大预览。发送图片保留最长边 640px 的有界 JPEG 预览，原图仍仅用于模型请求，不写入历史；旧历史只有文件名时不伪造预览。GIF 为静态首帧；无法解码或超过 1600 万像素的图片仍显示附件名。工具图片预览控制在 8KiB 编码前大小内，避免被通用工具输出上限截断；较旧输出被压缩后可重新打开远端文件。
- `canvas` 工具接受 `title`、`format`（`markdown` / `html`）及 `content`（最多 64KiB）。每次调用保留一份完整修订；可切换预览／源码并复制。HTML 仅允许沙盒内联脚本和样式，不具有宿主、设备、网络访问能力；使用 `ask` 单独获取需要返回模型的用户决定。这是会话内画布预览，不是 Hope 完整的项目、导出和快照平台。
- 上下文圆环详情支持压缩入口，运行期间禁用重复操作；显示压缩中／完成反馈，不显示 token 数字。后台面板增加耗时和复制，命令输出跟随底部，向上翻阅后保留位置。
- client 非 PTY 取消现在终止进程；Linux/macOS/BSD 使用独立进程组覆盖同组子进程，其他平台目前终止直接进程。定时输出与最终 flush 串行，缓冲区复用，避免尾部消息越过退出消息；流式 UI 滚动每动画帧合并一次。

参考：本地 BB `f51e6b0` 的主题、TerminalOutputBlock、ThreadTableOfContents；OMP `rpc-types.ts` 的独立图片／事件和会话命令边界；Hope `docs/architecture/infra/canvas.md` 的隔离预览设计。rdev 使用向后兼容的增量协议，不宣称可直接替换 OMP RPC 客户端。

- 前台轮次结果（done/error/stopped）持久化到会话历史；启动前先记录 running，重启恢复时将中断轮次显示为 stopped。重置会话会清除旧结果。该状态不表示所有后台任务成功。
- 桌面普通流与 GPU 流均按退避策略重连；设备暂时离线允许重试，密码错误停止自动重试，手动断开和设备切换取消旧连接重试。GPU 全局粘贴/IME 仅发送到当前获得焦点的桌面，鼠标悬停不抢焦点。

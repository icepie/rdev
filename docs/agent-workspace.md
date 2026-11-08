# Agent 工作区

## 会话与上下文

同一设备可以拥有多个独立会话；不同会话并发执行，同一会话的前台消息排队。浏览器断开不终止服务端执行，重新连接恢复历史与当前输出。

在输入框使用 `/compact` 压缩旧上下文，也可用 `/compact 保留错误和文件路径` 指定重点。压缩期间不向摘要模型提供工具；保留最新会话及完整工具调用/结果链。失败或取消不会替换原历史。上下文过短时返回无需压缩的错误。新建、重置、停止、模型切换等操作由输入区命令菜单和会话控件提供。

## 后台任务与子 Agent

模型可以调用以下工具，用户也可用自然语言要求创建任务：

- `bg`：在远程设备上运行后台 shell，立即返回任务 ID。任务默认运行至实际完成；可设置 0–86400 秒超时，0 表示不设期限。非零退出码记为失败。
- `jobs`：列出、读取、等待或取消本会话任务，也可用 `action="steer", id, message` 向运行中的 subagent 追加指导。单次等待最多 30 秒，等待超时不取消任务。
- Subagent 指导在模型响应或工具批次结束后按 FIFO 接入；最后一次边界之后收到的指导继续在同一子会话执行，不会复用主会话历史或扩大已授予的工具权限。每条最多 4096 字节，最多待处理 8 条。完成、取消或关闭后拒绝新指导；取消/失败时未消费的指导可通过 `jobs read` 的 `pendingGuidance` 查看。入口包括 Agent 的 `jobs` 工具及任务面板的“追加指导”。面板使用线程隔离的 `steer_job` / `job_steered` 请求回执，仅确认接收后清空未修改的草稿；拒绝、15 秒未确认或断线时保留内容。切换任务保留本面板内的草稿，任务结束后可复制，刷新页面不保证恢复这些任务草稿。兼容 OMP RPC 的 `get_subagents`、`get_subagent_messages`、`steer_subagent` 和 `cancel_subagent` 接口，接受 OMP 的 `type` 命令帧及 `subagentId`/`message`/`fromByte` 字段，返回标准 `response`/`success`/`data`/`error` 回执；消息读取返回 `rdev://subagent/<id>` 有界输出窗口，游标超出窗口时将 `reset` 置为 true，不伪装成 OMP 持久化 session 文件。
- OMP RPC 还提供 `negotiate_protocol`（当前版本 2）与 `get_available_commands`，客户端可以先发现 rdev 实际支持的 compact/subagent 命令和参数，再决定是否显示对应操作。
- `get_state` 返回指定线程的模型、流式/压缩状态、分开的 steering/follow-up 队列、后台任务 pending 状态、settled 状态、待办和 rdev 线程 URI；它读取服务端线程快照，不创建第二份会话状态。
- `get_available_models` 返回配置中的规范 `provider/model` 列表；`set_model` 在目标线程空闲时切换模型并保留历史，运行中或未知线程/模型返回标准错误，避免把切换伪装成排队消息。
- `abort` 取消目标线程当前轮次并清空待处理 follow-up；空闲线程返回 `{"aborted":false}`，运行中返回 `{"aborted":true}`，随后仍由浏览器线程生命周期广播唯一的 `stopped` 终止帧。
- `prompt` 接受 `message` 和可选 `thread`，在线程空闲时启动一轮模型对话；忙碌线程返回 `busy`，完成或提供方失败后向原连接发送一次关联 response，并继续广播正常 turn 生命周期。
- `steer` 与 `follow_up` 接受 `message` 和可选 `thread`，分别进入现有指导 FIFO 或普通 follow-up 队列；返回 admission response，详细排队/消费状态仍通过线程事件广播。
- `remove_queued_message` 兼容 OMP 的 `message` + `queue`（`steering`/`followUp`）匹配；rdev 额外接受 `queueId` 做稳定身份校验，返回 `removed` 和可恢复的 OMP `ImageContent[]`，不会删除后台通知或跨线程项目。
- `subagent`：创建独立模型上下文，委派明确任务和设备工具子集。默认使用可用的只读工具；使用父会话的模型/provider，最长运行 30 分钟。子 Agent 不递归创建后台任务或子 Agent。

`/jobs` 直接打开任务面板，`/jobs full` 展开完整任务名，`/jobs kill <id>` 取消单个任务，`/jobs kill all` 取消本会话当前任务。它们不经过模型。任务面板显示状态、输出和取消入口。任务完成后自动通知原会话；前台忙碌时排队，空闲时继续处理结果。每个会话最多同时运行 8 个任务，最多保留 64 个任务记录；达到保留上限时淘汰最早的已结束任务。每个任务的输出、结果及错误分别保留最后 32KiB，截断不切断 UTF-8 字符。取消请求发出后，执行结束才释放并发名额。服务端确认取消后，任务详情/列表的 `cancelRequested` 标记使面板显示“正在停止”，停止按钮及追加指导不可用；清理完成后才进入“已取消”。重复取消请求保持幂等，任务草稿保留供复制。任务启动、取消请求和完成会向同设备已连接的浏览器发送线程级 `jobs_changed` 通知，可见任务面板立即重读列表及所选任务详情；隐藏页面恢复可见后再同步，原有轮询仍作为恢复兜底。通知不携带任务快照，旧任务管理器不能恢复已重置的任务数据。状态推送和侧栏扫描由服务端追踪的异步任务执行；同一任务管理器的连续变更合并为一个待刷新标记，慢标签页不会阻塞任务创建或取消。

启动子 Agent 的授权包括所选能力在该任务内的使用，不依赖已经结束的父轮次审批。重置或删除会话会取消所属任务。启用会话持久化后，任务记录另存为同目录的 `<thread>.jobs.json`，最多恢复最近64条及有界输出、结果和未消费指导。启动、取消与完成时异步合并落盘，与浏览器推送独立；不定期检查点保存流式输出，突然退出可能丢失最后一次落盘后的状态和输出。重启只恢复记录，不重新执行 shell 或子 Agent；原 running 记录标为 failed，并明确提示服务端重启中断。重置、删除及会话 TTL 清理同时移除记录。落盘失败会记录日志并保留内存状态，下一次生命周期变更时重试；尚未成功保存的会话不参与空闲回收。

## 服务端协调模式

选择“服务端”（内部目标 `@server`），直接让 Agent 列出设备或指定设备执行任务，无需注册设备工具。`list_devices` 返回当前在线设备和工具参数；`device_tool` 接受明确的 device、tool、arguments，支持 shell、list_dir、read_file、write_file、edit、grep、glob。

入口位于 Agent 左侧栏底部的设备选择菜单，选择“服务端”；也可直接打开 `/agent?device=%40server`。设备菜单合并在线设备与历史会话，服务端不依赖已有会话，始终可选；仅有离线历史的设备保留显示。

服务端只负责协调，不执行宿主机 shell 或文件读写。后台 shell 同样需要明确指定远程设备；每次调用检查最新连接状态。管理令牌仍保护 Agent 入口；没有管理令牌时，服务端模式不能绕过远程设备的密码保护。

设备侧栏在 Agent、Files、Desktop、Terminal 共用同一设备源和 BB 行布局；首次加载显示状态，空列表只在请求完成后显示，设备列表请求失败可直接重试，不会把网络错误误报为“没有设备”。设备行支持 Home/End/上下箭头移动焦点，Enter/Space 保持选择操作，搜索过滤后导航只在当前结果集内循环。请求成功后若当前设备已离线，侧栏保留不可操作的离线行，避免把已打开的工作区身份误认为列表加载失败。

动画遵循 BB 的 150/220ms 曲线和 `prefers-reduced-motion`：侧栏、抽屉、弹窗和工作态 shimmer 在减少动画时立即切换或停用；加载 spinner 保留状态提示但停止旋转。

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

- 提供方/模型配置的 `api` 支持 `openai-completions`（默认）、`openai-responses` 和 `anthropic-messages`。Responses SSE 保留文本、推理摘要、函数调用、usage 和图片输入；使用 `store:false` 并持久化原始输出条目，限定同提供方/地址/模型回放。失败或取消后不保留可执行的未完成调用或原始回放条目；压缩与 subagent 继承所选协议。原始加密推理不会发送给浏览器，也不写入 chat-completions 请求。Anthropic 原生 Messages 支持文本/工具/图片及带签名和隐藏思考块，使用独立的同路由回放状态；模型的 `maxTokens`、`thinking.mode`、可选 `budgetTokens`/`supportsDisplay` 规则见 README。协议范围不包括 Responses WebSocket、供应商托管工具、Anthropic OAuth/Bedrock/Vertex 或 OMP RPC 全量兼容。

- 独立 Files 页与 Agent 共用文件预览：点击文件名预览文本、Markdown、HTML、CSV 表格、视频或图片，下载使用独立按钮；窄屏使用抽屉，提供刷新、下载和关闭。`/files` 的 `read` 请求携带 `deviceId`、`requestId`、`path`，返回 `file_content`，包含 `content`、`truncated`、可选 `encoding`/`mime` 或 `message`。沿用设备鉴权，不依赖 Agent 模型配置；过期响应不会替换当前预览。HTML/HTM 默认以隔离 iframe 预览，支持内联脚本/样式和嵌入资源；切换源码保留交互状态，刷新替换文档，暂不提供相对/远程资源加载。CSV 默认显示 BB 样式虚拟表格（前 500 数据行、100 列），可切换源码或复制已加载文本；图片可进入共用全屏缩放预览。切换文件清理预览/选区状态，桌面弹窗及窄屏抽屉内的图片关闭后都返回原预览。

- 工具失败状态保存并恢复；chat-completions/Responses 不新增此字段，Anthropic 将其映射为原生 tool_result.is_error；旧记录未保存该状态时不根据结果文字猜测。历史结果按所属助手响应匹配，避免调用 ID 跨轮复用串结果；仅含思考内容的消息也保留。

- 新增线程级 `assistant_start`：每次模型响应开始时结束上一条消息的流式展示，但不结束用户轮次或重置计时。工具调用前后的响应不再拼接，实时和重连回放按历史消息顺序显示；事件本身不生成空白消息。旧客户端可忽略此新增事件，旧服务端仍保留原有展示行为。

- `turn_start`、终态和历史条目增加可选 `display` 元数据：服务端时间、Steer 来源、结构化任务通知。元数据持久化但不进入 provider 请求；旧文本通知仍兼容。队列中的后台通知显示任务摘要。
- 新会话轮次显示服务端计时的耗时，重连后保留；旧历史没有时间数据时不猜测。Steer 参照 OMP 的边界注入：当前响应或整批工具完成后，按 FIFO 每次接入一条指导消息，优先于普通 follow-up，不取消已开始的工具。停止仍会取消执行并清空队列；斜杠命令保留独立命令轮次。新增线程级 `steering` 事件保留运行计时并结束上一段流式展示。`jobs wait` 收到本线程指导时提前返回最新任务快照（`waitInterrupted: true`），后台任务继续运行；指导队列由 Agent 边界接入，等待工具不会消费它。“指导当前回答”按钮以及带图片的 `/steer` 支持图文或纯图片；读取/转换附件期间不可发送，发送失败保留文字和图片。图片按同样的 FIFO 边界进入当前模型上下文，实时消息、重连回放和历史均展示有界预览；若回答已结束则作为普通新一轮接收，仍保留图片。尚未实现提供方原生的流内注入，不宣称完整 OMP RPC 或 OMP 全部等待/消息协调能力。
- 消息导航参考 BB `ThreadTableOfContents`，支持 Agent／用户消息索引、当前位置和会话内跳转；分屏按各自滚动容器定位。
- 未读使用标签页内的 `sessionStorage`，刷新保留未读及已观察到的运行状态；初次访问不把旧历史全部标为未读。已见过的会话通过持久化的 outcomeAt 标记识别断线期间完整执行的前台轮次，不依赖看到 running 状态；首次发现的历史不会自动标为未读。后台任务重启恢复不在此机制内。
- 发送前图片、发送后的缩略图和 `read_file` 图片共用放大预览。发送图片保留最长边 640px 的有界 JPEG 预览，原图仍仅用于模型请求，不写入历史；旧历史只有文件名时不伪造预览。GIF 为静态首帧；无法解码或超过 1600 万像素的图片仍显示附件名。工具图片预览控制在 8KiB 编码前大小内，避免被通用工具输出上限截断；较旧输出被压缩后可重新打开远端文件。
- `canvas` 工具接受 `title`、`format`（`markdown` / `html`）及 `content`（最多 64KiB）。每次调用保留一份完整修订；可切换预览／源码并复制。同一 HTML 修订在切换源码或主题时保持 iframe 实例，保留表单和脚本状态；内容变化才重建预览。主题通过单向 parent-to-frame 消息更新样式，不开放工具调用或宿主动作通道。HTML 仅允许沙盒内联脚本和样式，不具有宿主、设备、网络访问能力；使用 `ask` 单独获取需要返回模型的用户决定。这是会话内画布预览，不是 Hope 完整的项目、导出和快照平台。
- 上下文圆环详情支持压缩入口，运行期间禁用重复操作；显示压缩中／完成反馈，不显示 token 数字。后台面板增加耗时和复制，命令输出跟随底部，向上翻阅后保留位置。
- client 非 PTY 取消现在终止进程；Linux/macOS/BSD 使用独立进程组覆盖同组子进程；Windows 使用每命令独立 Job Object，暂停启动、加入 Job 后再恢复，取消覆盖子进程树（隔离 Wine 已验证，实机待验证）。其他平台终止直接进程。定时输出与最终 flush 串行，缓冲区复用，避免尾部消息越过退出消息；流式 UI 滚动每动画帧合并一次。

参考：本地 BB `f51e6b0` 的主题、TerminalOutputBlock、ThreadTableOfContents；OMP `rpc-types.ts` 的独立图片／事件和会话命令边界；Hope `docs/architecture/infra/canvas.md` 的隔离预览设计。rdev 使用向后兼容的增量协议，不宣称可直接替换 OMP RPC 客户端。

- 前台轮次结果（done/error/stopped）持久化到会话历史；启动前先记录 running，重启恢复时将中断轮次显示为 stopped。重置会话会清除旧结果。该状态不表示所有后台任务成功。
- 桌面普通流与 GPU 流均按退避策略重连；设备暂时离线允许重试，密码错误停止自动重试，手动断开和设备切换取消旧连接重试。GPU 全局粘贴/IME 仅发送到当前获得焦点的桌面，鼠标悬停不抢焦点。

- shell 输出在长度截断和旧历史自动压缩时分别缩短 stdout/stderr，保留退出状态与结构边界；旧版已压缩的 stdout 片段保留截断提示并隐藏原始包裹标签。

- 浏览器断线不会自动拒绝待审批工具或问题；原有五分钟超时、停止/重置/删除仍会结束等待。重连重放待审批卡，未明确批准不会执行。恢复历史后的滚动在 React 提交消息后调度，保留手动向上阅读的位置。

- 文字草稿按设备/会话保存在标签页 sessionStorage（每份最多 64K 字符），跨分屏挂载与刷新恢复；超长草稿仍可在当前页面编辑，但不落盘。图片附件仍仅在当前页面内存中保留。显式新建的空会话也会持久化，布局恢复去掉已删除或重复会话窗格。
- 会话信息面板不再显示累计 token 数，也不再遍历整段历史计算该数字；上下文圆环继续使用协议中的 usage 数据。
- 窄屏多会话改用横向会话标签，每次显示一个全宽窗格；隐藏窗格保持挂载并设置 inert，避免隐藏控件获取焦点。支持方向键、Home/End 切换；恢复宽屏时保留原分屏比例和草稿。仅当前可见窗格清除对应未读标记。
- 终端页复用相同窄屏窗格标签切换，标签显示窗格编号与设备；隐藏终端保持连接和输出，宽屏恢复分屏布局。Agent 和终端的当前窄屏标签会自动滚入可见区域。
- `read_file` 的完整 JPEG 预览按 OMP chat-completions 的工具图片规则发送给模型：先发送同批所有工具文本结果，再附加一条包含图片的 user 消息。转换仅发生在 provider 请求边界，界面与持久历史仍保留原预览；损坏或被旧压缩截断的预览不会作为图片发送。使用的仍是有界预览，模型需支持视觉输入。

- 删除确认与在线删除通知清理对应文字草稿；离线错过通知后，在该设备重新认证的完整会话列表上核对清理。缺失或异常列表不触发清理，其他设备、现存会话和尚未分配会话的草稿保留。

### 并发队列操作

队列快照和广播携带与文本一一对应的 `queueIds`。当前界面使用 `dequeue {thread, queueId, id}` 按稳定 ID 移除，已开始执行或已移除的消息不会误删后继项；请求方收到 `dequeued {id, thread, queueId, removed, text?}` 后，只有 `removed: true` 才把原始文本取回草稿。重复/失效回复不会重复插入文本，切换会话会清理待处理的取回回调。旧客户端的下标协议仅作为兼容入口保留，不提供稳定 ID 的并发保证；新界面对无 ID 的旧队列禁用操作。

取回编辑使用 `restore: true`，成功的 `dequeued` 回复可携带 `images`（原始 name/mime/base64）。图片仅返回请求方，不进入队列广播；纯移除操作不回传图片。编辑器恢复原图预览和可重发的数据，取回期间阻止新增附件或发送；已有图片草稿时需先发送或移除，以免覆盖附件或超过四张限制。纯图片队列项显示“图片附件”。正常执行、边界追加和移除队列项都会清空腾出的数组槽，避免残留引用继续保留原图。原始图片仍不写入持久历史。

Agent 草稿、已发送图片及 read_file 图片使用 BB 全屏预览。支持滚轮/触控缩放、放大后拖动、适应屏幕、Esc 关闭；同一条消息或草稿有多张图时支持左右按钮/方向键循环切换。关闭恢复触发缩略图的焦点。缩放上限 8 倍，切图重置视图；透明图片显示跟随主题的棋盘格。

- 工具参数按声明的 JSON Schema 校验：直接模型调用在审批/执行前检查必填字段、类型、枚举和范围；错误作为配对工具结果返回，模型可在下一轮修正。服务端模式的 device_tool 在执行所选内层工具前也校验其参数。write_file 缺少 content 或传 null 会失败，明确传空字符串仍表示写入空文件。不会自动改写参数或套用 OMP 的参数修复规则。

- 视频预览支持浏览器可解码的 MP4/M4V/WebM/OGV/MOV，使用原生控制条和 HTTP Range，无需整文件下载到内存。需要支持 `length` 下载范围的设备客户端；旧客户端会提示播放失败。预览地址只授权单个设备文件，有效一小时，设备密码变更或服务端重启后失效，刷新可重新获取。每次设备读取最多512KiB、超时15秒，最多16个并发 HTTP 媒体请求；不提供转码。

- 完整 HTML/HTM 文件可从预览工具栏“在浏览器中打开”。新标签页与内嵌预览采用响应头 CSP 沙盒，不开放宿主存储、宿主 API、外部网络或表单提交。支持目录约束的新客户端可加载 HTML 所在目录及其子目录的相对 CSS、脚本/模块、图片、字体、媒体和 JSON；模块 import 与同一授权路径内 fetch 可用。客户端使用 OpenInRoot 阻止目录和符号链接越界，每次读取确认 downloadRoot 回执；隐藏路径、非网页资源扩展名和目录列表不开放。地址为一小时 bearer 授权，密码变化或服务端重启会失效。旧客户端保持原单文件内联预览。超出预览大小限制时不提供截断网页。相对资源以所打开 HTML 的父目录为边界，不提供项目根绝对路径重写、开发服务器代理或服务端动态路由。

- Web添加AVIF/SVG/ICO图片时先转换成PNG，以统一模型输入和历史预览支持；保留文件名和透明度，编码超4MiB时缩小尺寸。超过1600万像素或浏览器无法解码则提示失败；静态转换不保留动画或矢量信息。PNG/JPEG/GIF/WebP/BMP附件不转换原字节。

- Agent 与 Files 的文件预览在“预览／源码”之间切换时分别保留阅读位置；源码首次使用才挂载。CSV 隐藏时保留测量尺寸，防止虚拟行高度变化导致滚动偏移；HTML 沿用保留 iframe 的行为。位置仅在当前文件组件存续期间保留，刷新加载、关闭或重载页面不保证恢复。

- HTML 远端预览加载前通过 HEAD 检查授权/设备响应，失败显示可重试提示；新目录资源服务的 HEAD 只读取有界元信息，不下载 HTML 正文。文件图标沿用 BB 的 160ms 延迟加载动效，并支持 reduced-motion。切换预览/源码保留 iframe，不重复检查；内容或预览 URL 改变时替换 iframe 并取消旧检查。工具栏外部打开和 iframe 共用本地授权 URL 校验，兼容旧单文件及新目录入口。HEAD 与随后 GET 之间的设备状态变化仍可能由 iframe 自身展示错误页。

- 对照 OMP 的 promote_queued_message，运行中的普通排队消息可通过行内“用于指导当前回答”提升到指导队列末尾，保留稳定 ID、图片原字节和已有指导顺序；不会取回或覆盖输入框草稿。操作通过 promote_queue / queue_promoted 和 queueModes 同步到各会话窗格；重复提升仍在队列中的消息不会重复插入。已消费、跨线程、停止后的消息不能提升；会话命令、后台通知、要求切换模型的消息及正在执行命令的轮次保持独立轮次语义。该行为使用 rdev 协议，不表示完整 OMP RPC 兼容。

### OMP 子 Agent 帧适配边界

通过已认证的 rdev `/agent?device=…` WebSocket，可发送原生 OMP 形式 `{"type":"get_subagents","id":"r1"}`、`{"type":"steer_subagent","id":"r2","subagentId":"…","message":"…"}` 和 `{"type":"cancel_subagent","id":"r3","subagentId":"…"}`。可选 `thread` 是 rdev 扩展，省略时使用该连接当前会话。前两轮说明中把 `type:"request",command` 称为 OMP 原生格式不准确；该包装形式只是保留兼容入口。

RPC 结果放在 `data`：列表为 `{subagents:[]}`，取消为 `{cancelled:boolean}`。列表只含当前会话中未请求取消的运行子任务，提供 OMP 必需的 id/index/agent/agentSource/status/lastUpdate；index 是此次列表位置，agent 为内置 subagent，lastUpdate 为已记录的启动时间，不代表实时输出时间。空结果为数组，不能是 null。重复取消及未知任务返回 false，shell 任务拒绝；状态检查与取消在同一锁内完成。OMP 的指导请求允许省略 id，原生浏览器指导仍要求请求 ID。

这三个命令的帧适配不等于完整 OMP 运行时：连接鉴权、会话事件仍为 rdev 协议，没有 OMP stdio 传输、子任务完整 transcript/订阅、注册 agent 文件和扩展命令展开。取消只请求停止，资源名额直到执行清理完成才释放，不宣称 OMP 完整生命周期或持久 tombstone 语义。

RPC 帧由独立命令白名单分发，复用设备认证门槛。未知命令（含尚未实现的 get_subagent_messages）返回关联的 unsupported_command；字段缺失/类型错误、混合 op 与 type 帧返回 invalid_request。兼容包装的 command 也不能调用 reset 等浏览器操作。子任务日志没有完整 transcript 及稳定字节游标，因此暂不把滚动截断输出伪装成 OMP 消息文件。

### 原生 compact RPC

接受 `{"type":"compact","id":"request-id","customInstructions":"保留路径"}`（可选 `thread` 指定会话），也保留旧 `type:"request", command:"compact"` 包装。认证、线程存在性和运行中检查通过后执行现有压缩生命周期；不会立即返回 accepted。完成收尾后只向原请求连接发送一次关联 `response`，成功的 `data.summary` 为实际摘要；失败、取消或 reset/delete 使旧轮次失效时返回 `success:false`。浏览器订阅者仍收到线程生命周期及替换后的历史，RPC 回执不会广播。断线不会取消已经接收的压缩，也不提供跨连接回执重放。

这是 OMP 命令帧和完成语义的部分适配：rdev 没有 OMP 持久化 entry ID 和压缩前 token 估算，因此结果不伪造 `firstKeptEntryId` / `tokensBefore`；不能作为完整 `CompactionResult` 的替代。UI 不新增 token 数。

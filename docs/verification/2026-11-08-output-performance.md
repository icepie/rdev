# 输出路径性能基线与优化

环境：Linux amd64，AMD Ryzen 9 9950X，Go `go1.27.3-X:nodwarf5`。2026-11-08 本地运行，默认 GOMAXPROCS=32。以下为三次样本中位数；这是单进程微基准，不是网络、模型或浏览器端到端 SLA，也没有做独占 CPU 的实验控制。

## 后台任务输出

每次操作累计 256KiB，保留最后 32KiB，每 256 个 delta 读取一次不可变快照，完成时再读取一次。`legacy` 在同一测试二进制中保留修改前的字符串拼接/截断算法作为对照；`buffer` 使用新环形缓冲区。B/op 为累计分配字节，不是常驻内存。

| 每次追加 | 原算法 ns/op | 新算法 ns/op | 原算法 B/op | 新算法 B/op | 原/新 allocs/op |
| --- | ---: | ---: | ---: | ---: | ---: |
| 32B | 29,164,617 | 137,614 | 311,276,068 | 1,159,810 | 8,205 / 51 |
| 1KiB | 1,080,312 | 24,243 | 9,733,432 | 179,712 | 255 / 12 |
| 32KiB | 56,829 | 13,454 | 458,761 | 98,306 | 7 / 3 |

原实现对每个小 delta 重建整段尾部字符串。现在写入有界字节缓冲，只在 Read/完成时生成字符串；完成后释放活动缓冲。快照不会被后续写入修改，UTF-8 尾部规则保持兼容。大结果/错误截断另外复制保留尾部，避免短字符串切片长期持有完整大字符串的底层内存；该收尾复制不是上表的流式追加基准内容。

## 客户端合并输出基线

每次操作同样写入 256KiB；内存 transport 只统计已编码帧，不保留数据、不引入网络等待。为隔离字节阈值路径，计时器间隔设为一小时并在末尾显式 flush；真实 5ms 调度延迟不在此基准内，最终 flush 的并发顺序有单独行为测试。

| 每次写入 | ns/op | B/op | allocs/op | frames/op |
| --- | ---: | ---: | ---: | ---: |
| 32B | 127,871 | 327,777 | 194 | 64 |
| 1KiB | 32,247 | 327,777 | 194 | 64 |
| 8KiB | 20,854 | 311,392 | 34 | 32 |

此表是客户端现状基线，本轮未修改客户端输出合并生产代码，不能描述为本轮客户端吞吐提升。

## 复现与正确性

```sh
go test ./internal/server -run '^$' -bench '^BenchmarkAgentJobOutput$' -benchmem -count=3 -benchtime=200ms
go test ./internal/client -run '^$' -bench '^BenchmarkCoalescingOutput$' -benchmem -count=3 -benchtime=200ms
go test -race ./internal/server
go test -race ./internal/client
```

随机分块回归覆盖 1B/7B/1KiB/32KiB/64KiB、中文和 emoji 的任意字节边界、超限输入、重复环绕及旧快照不变。已有运行中读取、完成、取消、异常、保留淘汰、后台与 subagent 测试仍通过。完整服务端和客户端 race suite、服务端构建通过。

## 前台会话重连回放

服务端原先逐条保留每个 delta/reasoning 的完整 JSON。现在只合并小于 256B 的相邻同类、同线程文字增量，每块最多 8KiB；较大增量直接保存原帧。工具、审批、指导和终止帧都会结束当前块。实时 WebSocket 推送频率不变，正文不截断。快照复制列表并单独编码当前待写块，后续追加和 reset 不会修改旧快照。

每次操作产生 256KiB 文本；两条路径都逐增量序列化实时推送帧，最后生成一份独立回放快照。下表为三次 200ms 样本中位数。与同机测试并行运行，未隔离 CPU；时间列不能证明端到端提速。replay bytes 为快照中 JSON 字节总和，不含 slice/对象头，不等于 RSS；B/op 是累计分配。

| 增量 | 原/新回放帧 | 原/新 replay bytes | 原/新 ns/op | 原/新 B/op | 原/新 allocs/op |
| --- | ---: | ---: | ---: | ---: | ---: |
| 32B | 8192 / 32 | 606208 / 263488 | 7494343 / 7423486 | 18386015 / 18352530 | 24639 / 24821 |
| 1KiB | 256 / 256 | 272896 / 272896 | 457816 / 361078 | 843574 / 843620 | 780 / 780 |

明确收益是 32B 小增量场景的回放帧数减少 256 倍，JSON 体积减少约 56.5%；并不宣称总分配次数下降，测试中小增量分配次数略增。1KiB 场景采用直接保存路径，无体积收益。尚未测量真实浏览器重连延迟或服务端 RSS。

回归覆盖中文/emoji/引号/换行、思考与正文切换、工具/审批/指导边界、大增量、线程边界、旧快照不变和 reset 清理。已有真实 SSE→WebSocket 思考重连回归继续通过。

```sh
go test ./internal/server -run '^$' -bench '^BenchmarkAgentReplay$' -benchmem -count=3 -benchtime=200ms
go test -race ./internal/server -run 'TestAgentReplay|TestAgentReasoningStreamsAndReplaysBeforeCompletion|TestResetRejectsLateTurnEvents|TestAgentStop|TestApprovalSurvivesBrowserReconnect'
```

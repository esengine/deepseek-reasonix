# 桌面轨迹

[English](DESKTOP_TRAJECTORY.md)

轨迹面板是桌面的活动账本：一次运行实际做了什么、按顺序发生、每段活动跑了多久。它
是对 Transcript 已经在消费的同一份 wire 事件的只读投影，因此不新开第二条事件通路，
也从不改变模型看到的内容。

```text
标签页控制器 ─▶ 事件 sink ─▶ 唯一事件处理函数 ─┬─▶ Transcript reducer
                                              └─▶ 轨迹账本 ─▶ 面板
持久账本 ─▶ TurnEventsForTab ─▶ 缺口修复 ─────┘   （同一个处理函数）
```

## 面板

右侧工作区新增「轨迹」标签页（`rightDock.trajectory`），可从 Dock 的加号菜单、标签
选择器和启动器打开。表格里一段活动一行：序号、起始偏移、共享时间轴上的条、记录类型
和负载。条从活动开始处起画，长度就是它跑了多久，因此并排重叠的条就是并行跑的活动
——一个回合的条会罩住它内部的模型轮次与调用。

记录类型：`turn`、`user`、`assistant`、`model_round`、`tool`、`usage`、
`approval`、`ask`、`guardian`、`compaction`、`maintenance`、`recovery`、`phase`、
`steer`、`completion`、`notice`。对话正文（`text`、`reasoning`）与低信息量的帧属于
Transcript，不占行；未知类型一律忽略，因此更新的宿主不会把这个视图弄坏。

## 一段活动就是一行

开始一段活动的事件开一行，结束它的事件改这一行，所以一次工具调用与其结果不会出现
两行。部分 dispatch（参数仍在流式传输）不是第二次调用，进度事件归到该调用已有的那
一行上。命名了一段没人开过的活动的帧会被丢弃，而不会被画成新活动。

## 时间

每一行都记录自己的时间来源。`kernel` 表示宿主测出来的：回放信封的 `createdAt`、回合
的 `turnStartedAt`、工具的 `startedAt`/`durationMs`。`receipt` 表示由本客户端在事件
到达时打点，并按两个时钟的偏移每个标签页校正一次。没有测量到时长的一行画成刻度，绝
不画成编造宽度的条；仍在运行的活动不会被安上一个它还没到的完成时刻。

## 覆盖度

表尾会说明这些行覆盖了多少，因为被截断记录的最后一行与一场短会话的最后一行看起来完
全一样：

- `complete` —— 持久记录从其第一条事件起被完整回放。
- `compacted` —— 宿主已把更早的事件折叠掉（回合事件账本在 8 MiB / 4096 条事件时折
  叠），所以表格从回放仍能到达的地方开始。
- `live_only` —— 没有读到持久记录，这些行只是本次连接看到的。宿主若没有
  `TurnEventsForTab` 绑定，就报这个。
- `unread` —— 覆盖度的读取还没落地。它不表示完整。

本地裁剪掉的前缀会单独声明：面板每个标签页最多保留 5000 行，被它丢掉的那部分宿主可
能仍然持有。

## 导出

工具栏把行导出为 JSON：覆盖度、轴的跨度，以及每行的
`seq/at/dur/kind/tool/turnId/stamped/open/text/detail`。文件会比造它的窗口活得久，所
以它自带覆盖度。导出走壳的导出选择器（`PickExportFile` + `SaveExportFile`）。

## 限制

- 未做虚拟化：面板渲染它持有的每一行，由本地上限约束。长会话是需要盯住的情形。
- 还没有缩放、拖选或搜索；这条轴目前是只读的。
- 没有宿主时间戳的活动（模型轮次、usage、notice、压缩、阶段）按接收时刻定位，所以它
  们的条会带上投递延迟；回合与工具不受影响。

## 验证

`pnpm test:trajectory`（折叠语义、覆盖度状态、导出负载）、`pnpm typecheck`、
`pnpm lint:hooks`、`pnpm check:css`、`pnpm check:app-layers`、
`pnpm check:scroll-writer`、`pnpm check:bundle`，以及
`node bench/trajectory-panel.mjs`：它在真实浏览器里驱动一次脚本化的回合，断言面板能
从 Dock 打开、一次调用与其结果只占一行、没有持久账本的宿主报 `live_only`。

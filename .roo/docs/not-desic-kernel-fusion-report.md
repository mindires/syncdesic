# Not-Desic：代码库矛盾与可 Kernel Fusion 的调查点报告

> 调查日期：2026-07-23
> 调查范围：`lib/model/` + `internal/db/sqlite/` + `internal/db/interface.go`
> 基准：Syncdesic v0.1.0（三次 cache-over-blocks commit 落地后）

---

## 目录

1. [P0：两条调度路径并行但只有一条在用](#1-p0两条调度路径并行但只有一条在用)
2. [P0：insertBlocksLocked 死代码](#2-p0insertblockslocked-死代码)
3. [P1：BlockIndexing 语义漂移](#3-p1blockindexing-语义漂移)
4. [P1：AllNeededBlockHashes 数据库层无人消费](#4-p1allneededblockhashes-数据库层无人消费)
5. [P2：reconcileBlockIndex 启动时的 blockIndexEmpty 多余检查](#5-p2reconcileblockindex-启动时的-blockindexempty-多余检查)
6. [P2：queue 子系统的 content-addressed 融合](#6-p2queue-子系统的-content-addressed-融合)
7. [P3：processNeededByHash 仍走 AllNeededGlobalFiles](#7-p3processneededbyhash-仍走-allneededglobalfiles)
8. [冷启动 cache miss 路径的持久风险评估](#8-冷启动-cache-miss-路径的持久风险评估)
9. [总结优先级](#9-总结优先级)

---

## 1. P0：两条调度路径并行但只有一条在用

### 现状

`pullerIteration`（[`folder_sendrecv.go:374`](../lib/model/folder_sendrecv.go:374)）第 414 行调用：

```go
fileDeletions, dirDeletions, err := f.processNeeded(ctx, dbUpdateChan, copyChan, scanChan)
```

从不调用 `processNeededByHash`。

### 已实现的 infrastructure

- `processNeededByHash`（[`folder_sendrecv.go:145`](../lib/model/folder_sendrecv.go:145)）——完整实现，跳过 queue 管线
- `AllNeededBlockHashes`（[`folderdb_needblockhash.go:26`](../internal/db/sqlite/folderdb_needblockhash.go:26)）——完整的 SQL 查询 + 分组逻辑
- `DB.AllNeededBlockHashes`（[`db_folderdb.go:247`](../internal/db/sqlite/db_folderdb.go:247)）——完整路由
- `db.NeededBlockHash`（[`interface.go:137`](../internal/db/interface.go:137)）——完整类型定义
- 全套单元测试（`folder_sendrecv_hash_test.go` 和 `db_needblockhash_test.go`）

### 影响

- 100% 的 content-addressed puller 上层是空中楼阁
- `processNeededByHash` 的 1+N SQL 优化从未在生产中生效
- `AllNeededBlockHashes` 数据库查询从未被消费
- 所有调度仍走 `processNeeded` 的 1+3N SQL 管线（`queue.Push → queue.Pop → GetGlobalFile ×1 → fileAvailability ×1 → handleFile ×1`）

### 建议

- **方案 A（激进）**：`pullerIteration` 中把 `processNeeded` 调用替换为 `processNeededByHash`，删除 `processNeeded` 和 queue 逻辑
- **方案 B（保守）**：保留两条路径，通过 `BlockIndexing` 配置切换——`true` 走 `processNeededByHash`，`false` 走旧路径
- **方案 C（渐进）**：保留 `processNeeded` 的删除/目录/符号链接处理，仅在 regular file 路径上切换到 content-addressed dispatch

**Kernel Fusion 价值**：消除 3N 冗余 SQL → 每次 pull iteration 减少 N×2 次 SQL（N = needed files count）

---

## 2. P0：`insertBlocksLocked` 死代码

### 现状

[`folderdb_update.go:386`](../internal/db/sqlite/folderdb_update.go:386)：

```go
func (*folderDB) insertBlocksLocked(tx *txPreparedStmts, blocklistHash []byte, blocks []protocol.BlockInfo) error {
```

Syncdesic 的 Update 路径（第 155 行）已跳过此调用：

```go
if device == protocol.LocalDeviceID && !options.SkipBlockIndex {
    s.blockCache.notify(f.Name, f.BlocksHash, f.Blocks)
}
```

### 影响

- `insertBlocksLocked` 函数体完整保留但永不调用
- 无数据风险，但增加了代码维护成本和误读风险
- `hdd_sim_bench_test.go` 第 143 行的注释仍写 "triggers insertBlocksLocked"（但 benchmark 实际上用的是 `WithSkipBlockIndex`）

### 建议

- 删除 `insertBlocksLocked` 函数
- 删除 `blockIndexEmpty` 中查询 blocks 表的 EXISTS 逻辑（改为直接返回 `false` 或删除调用）
- 保留 blocks 表 schema 以保上游兼容性

---

## 3. P1：`BlockIndexing` 语义漂移

### 现状

[`folderconfiguration.go:89`](../lib/config/folderconfiguration.go:89)：

```go
BlockIndexing bool `json:"blockIndexing" xml:"blockIndexing" default:"true"`
```

Syncdesic 中的实际行为：

| `BlockIndexing` | 效果 |
|---|---|
| `true`（默认） | `updateLocals` 不传 `SkipBlockIndex` → Update 路径执行 `cache.notify`（[`folderdb_update.go:155`](../internal/db/sqlite/folderdb_update.go:155)） |
| `false` | `updateLocals` 传 `SkipBlockIndex` → Update 路径跳过 `cache.notify` → cache 永不更新 |

### 问题

- `BlockIndexing=false` 时，cache 在正常运行时永远不会被预热
- 冷启动后 cache 空 + 运行时也不写入 = `AllLocalBlocksWithHash` 永远 miss
- 当前仅 `reconcileBlockIndex` 启动时调用的 `PopulateBlockIndex` 能预热一次
- 但 pull iteration 中新出现的文件（远端推送的）不会被 `PopulateBlockIndex` 覆盖——它们是在启动后出现的

### 建议

- **移除 `BlockIndexing` 配置与 `cache.notify` 的绑定**：cache notify 应该与 `BlockIndexing` 无关——cache 是 Syncdesic 的核心，不是可选项
- 将 `BlockIndexing` 的语义从"是否写入 block index"重构为"是否启用 content-addressed puller"（如果既要保留配置）

---

## 4. P1：`AllNeededBlockHashes` 数据库层无人消费

### 现状

完整实现链：

```
DB.AllNeededBlockHashes()           ← db_folderdb.go:247
  → folderDB.AllNeededBlockHashes() ← folderdb_needblockhash.go:26
    → neededBlockHashLocal/Remote() + groupBlockHashRows()
```

### 消费者

- `processNeededByHash`（唯一逻辑上的消费者）——但该函数内部调用的还是 `AllNeededGlobalFiles`，不是 `AllNeededBlockHashes`
- 测试文件 `folder_sendrecv_hash_test.go` 和 `db_needblockhash_test.go`——验证了功能但不代表生产调用

### 影响

- 完整的 SQL 查询 + 分组逻辑 + 测试覆盖，零生产调用
- `NeededBlockHash` 类型定义（[`interface.go:137`](../internal/db/interface.go:137)）成为 dead code

### 建议

- 将 `processNeededByHash` 改造为真正使用 `AllNeededBlockHashes`：
  1. 用 `AllNeededBlockHashes` 替代 `AllNeededGlobalFiles` 作为入口
  2. 按 blocklist hash 分组 dispatch，而不是按 file 逐条迭代
  3. 这样可以实现真正的 block-first puller iteration

---

## 5. P2：`reconcileBlockIndex` 启动时的 `blockIndexEmpty` 多余检查

### 现状

[`folderdb_update.go:338`](../internal/db/sqlite/folderdb_update.go:338)：

```go
func (s *folderDB) PopulateBlockIndex() error {
    empty, err := s.blockIndexEmpty()  // SELECT EXISTS (SELECT 1 FROM blocks)
    if err != nil || !empty {
        return err
    }
    // ... 从 blocklists protobuf 重建 cache
```

Syncdesic 下 blocks 表始终为空 → `blockIndexEmpty()` 永远返回 `true` → `PopulateBlockIndex` 每次启动都全量重建。反复重建 cache，浪费 protobuf 反序列化开销。

### 建议

- 添加 cache 持久性标记（例如文件 `cache_ready.flag`），避免每次启动全量重建
- 或依赖 Update 路径的实时 notify，完全移除启动时的 `PopulateBlockIndex`

---

## 6. P2：queue 子系统的 content-addressed 融合

### 现状

queue 子系统的职责：

1. **调度排序**：`Push(file, size, modified)` 按 PullOrder 排队
2. **API 接口**：`BringToFront` / `Jobs(page, perpage)` 被 GUI 和 API 调用
3. **进度追踪**：`progress` 列表记录进行中任务

[`queue.go`](../lib/model/queue.go) 是内存中简单的 slice 操作，不涉及 SQL。

### 问题

- `processNeededByHash` 完全跳过 queue，但 API 层 (`BringToFront`/`Jobs`) 仍绑在 queue 上
- 如果 `processNeededByHash` 成为默认路径，需要为 API 层提供替代方案

### 建议

- queue 保留仅用于 API 兼容（BringToFront/Jobs）
- 调度决策从 queue 转移到 content-addressed 路径
- API 层的 queue 数据可以从 `sharedPullerState` 的活跃 pullers 重建，不需要独立的 queue 存储

---

## 7. P3：`processNeededByHash` 仍走 `AllNeededGlobalFiles`

### 现状

[`folder_sendrecv.go:151`](../lib/model/folder_sendrecv.go:151)：

```go
for file, err := range itererr.Zip(f.model.sdb.AllNeededGlobalFiles(f.folderID, protocol.LocalDeviceID, f.Order, 0, 0)) {
```

函数名叫 `processNeededByHash`，但迭代入口仍然是 `AllNeededGlobalFiles`——按全量 FileInfo 迭代。

### 分析

- 当前实现之所以这样，是因为需要 `full FileInfo` 做 routing decisions（删除/目录/符号链接的处理）
- `AllNeededBlockHashes` 只返回 `(blocklist_hash, [names])`，没有 FileInfo
- 但 regular file 路径可以分叉：用 `AllNeededBlockHashes` 做 block-first dispatch，非 regular file 仍走旧路径

### 建议

- 在 `processNeededByHash` 中将 regular file 与 non-regular file 分流：
  - 目录/删除/符号链接：仍用旧的 routing（需要 full FileInfo）
  - Regular file：从 `AllNeededBlockHashes` 获取文件列表，按 block hash 分组 dispatch

---

## 8. 冷启动 cache miss 路径的持久风险评估

### 现状

`AllLocalBlocksWithHash`（[`folderdb_local.go:106`](../internal/db/sqlite/folderdb_local.go:106)）cache miss 时返回空迭代器：

```go
if !ok {
    return func(yield func(db.BlockMapEntry) bool) {}, func() error { return nil }
}
```

### 影响链路

```
cache miss → copyBlockFromFolder (folder_sendrecv.go:1569) 返回 false
  → puller 认为本地无此 block → 走远端 pull → 重复下载
    → 最终 SHA-256 校验成功 → 不影响数据完整性
      → 但浪费带宽 + 时间
```

### 持续风险评估

| 场景 | cache 状态 | 影响 |
|---|---|---|
| 新文件由远端推送 | cache 温热（Update notify 已执行） | 零影响 |
| 新文件由本地扫描产生 | cache 温热（扫描→Update→notify） | 零影响 |
| 首次启动，已有文件 | cache 冷 → `PopulateBlockIndex` 预热 | 首次 pull 可能 miss |
| 首次启动后新出现文件 | Update notify 实时预热 | 零影响 |
| `BlockIndexing=false` | cache 永远冷 | 所有 block 都走远端 pull |

最大风险在 `BlockIndexing=false` 配置下的用户。但此配置在 Syncdesic 中不应该被设为 `false`。

---

## 9. 总结优先级

| 编号 | 项目 | 优先级 | 类型 | 收益 |
|------|------|--------|------|------|
| #1 | `pullerIteration` 启用 `processNeededByHash` | **P0** | Kernel Fusion | 消除 2N 冗余 SQL 查询 |
| #2 | 删除 `insertBlocksLocked` 死代码 | **P0** | 清理 | 减少维护成本 |
| #3 | 修正 `BlockIndexing` 与 cache notify 的绑定 | **P1** | Bug Fix | 防止 `BlockIndexing=false` 时 cache 失效 |
| #4 | `AllNeededBlockHashes` 接入生产管线 | **P1** | Kernel Fusion | 实现真正的 block-first dispatch |
| #5 | 消除 `blockIndexEmpty` 的 blocks 表查询 | **P2** | 优化 | 减少启动时的一次多余 SQL |
| #6 | queue 子系统的 content-addressed 适配 | **P2** | 重构 | 为删除 queue 做准备 |
| #7 | `processNeededByHash` 改为使用 `AllNeededBlockHashes` | **P3** | 重构 | 更彻底的 content-addressed |
| #8 | 冷启动 protobuf 扫描兜底 | **推迟** | 优化 | 降低首次 pull miss 概率 |

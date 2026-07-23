---
title: Syncdesic Ignore System — 设计提案
date: 2026-07-23
status: draft
---

# Syncdesic Ignore System — 设计提案

## 问题

上游 Syncthing 的文件 ignore 系统自 2014 年起未做实质性演进。

- `.stignore` 语法固定为 `gobwas/glob` + 自创 `(?i)` / `(?d)` / `#escape` 前缀
- issue `#2491`（Next Gen Ignores）2015 年由 calmh 开启，标记为 v1.0 里程碑，十年后 frozen 关闭
- 社区要求 `.stignore` 同步（`#2353`），calmh 回复"你自己用 `#include` 凑合"
- 性能层面：ignore 模式用 `gobwas/glob` 编译，扫描时逐路径匹配，不支持 content-level 过滤

根本原因是范式分歧：calmh 认为文件同步是 `路径匹配` 问题，Syncdesic 认为是 `内容寻址` 问题。上一轮 cache-over-blocks 在数据库层纠正了此分歧——现在 ignore 层需要对齐。

## 约束

- 不能破坏 BEP 协议兼容性——Syncdesic 节点必须能与上游 Syncthing 节点互通
- `.stignore` 不能改名——上游不认识新文件名，folder 内不同步的文件会因上游不认识而被同步
- 不支持 content-based ignore == ignore 系统永远停留在 2014 年设计

## 为何不抄 gitignore

Git 本身也是 path-based 匹配。`gitignore` 和 `stignore` 的区别仅限于语法细节（first match vs last match、`**` 语义边界），迁移到 gitignore 的收益仅是 `熟悉感`，不解决任何架构问题。代价是丢掉上游兼容性，不值。

根本问题是 `path-based` 本身——不是格式。

## 设计：双文件 + content 扩展

### 文件角色

- `.stignore`：上游兼容层，原封不动保留。语法、语义、加载机制不变
- `.sdignore`：Syncdesic 增强层，新增文件。gitignore 标准语法 + content hash 扩展

`.sdignore` 与 `.stignore` 位于同一目录（folder root），两者独立加载、合并匹配。同一条路径后者覆盖前者。

`.sdignore` 的 gitignore 语法支持全 13 条 git 官方 pattern 规则（`#` `!` `/` `*` `?` `[abc]` `**/` `/**` `a/**/b` etc.）。采用 wildmatch 引擎（`git-pkgs/gitignore` 或等位实现），非 `gobwas/glob`——因为 git 的 `**` 语义与 glob 库的 `**` 语义在边界情况有差异。

### Content hash 扩展语法

```
# content hash 匹配——忽略特定内容的文件
?ch sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855

# block hash 匹配——忽略包含特定 block 的文件
?bh sha256:a1b2c3d4...

# 否定 content ignore
!?ch sha256:abc...
```

`?` 是 gitignore 语法的单字符通配符（匹配任意一个字符），`?ch` 在 git 语义下等价于 `任意字符 + ch`——这在文件路径匹配中无意义，因此作为扩展信号不会与任何合法路径冲突。解析器在 gitignore pass 中发现以 `?ch` 或 `?bh` 开头的行时，转入 content 路径处理。

`?ch` 匹配 `BlocksHash`（整个文件的 block list 的 SHA-256），`?bh` 匹配单个 `Block.Hash`。两者由 `ContentMatcher` 管理：

```go
type ContentMatcher struct {
    contentHashes map[[32]byte]bool
    blockHashes   map[[32]byte]bool
}

func (m *ContentMatcher) MatchContent(blocksHash []byte) ignoreresult.R
```

### Matcher 合并结构

```go
type Matcher struct {
    upstream  *stignore.Matcher       // .stignore，原封不动
    syncdesic *sdignore.Matcher       // .sdignore，gitignore + content
}

// Match 返回对 path 的 ignore 决定
// .sdignore 的匹配结果覆盖 .stignore——无论后者是否先匹配
func (m *Matcher) Match(file string, isDir bool) ignoreresult.R {
    upRes := m.upstream.Match(file)
    sdRes := m.syncdesic.Match(file, isDir)
    if upRes.IsIgnored() && !sdRes.IsIgnored() {
        return sdRes                     // sdignore un-ignored
    }
    if !upRes.IsIgnored() && sdRes.IsIgnored() {
        return sdRes                     // sdignore ignored
    }
    return upRes                         // 无冲突或两者一致
}
```

`MatchContent` 独立调用，不进入 path 匹配流程——调用者在扫描或拉取时已知 `BlockHash` 时显式调用。

## 扫描流程集成

`lib/scanner/walk.go` 已有 `ignores.Match(filename)` 检查——在此检查后追加 `contentMatcher.MatchContent(fi.BlocksHash)`。

需要确认 `walk.go` 在扫描本地文件时是否能获得 `BlocksHash`。如果扫描时尚未计算 hash（处于 `ReadInfos` 阶段），则 content match 推迟到 puller 阶段——反正 content-ignored 的文件仍会出现在 file index 中（以便与对端保持一致），只是数据层跳过其 block 传输。

## Puller 流程集成

Puller 在拉取 block 时已知每个 object 的 `BlocksHash`。content-ignored 的 object 在 `need.go` 或 `puller.go` 的 block request 层级被过滤——不拉其 block list，不占带宽，不占存储。metadata 层面该文件仍然存在（名字、路径、修改时间），仅数据不到本地。

这和 selective sync 的"根本没有这个路径"不同——content ignore 保留了元数据一致性，避免对端认为文件已删除。

## 兼容性矩阵

| 节点 A    | 节点 B    | `.stignore` | `.sdignore` | 效果                                         |
| --------- | --------- | ----------- | ----------- | -------------------------------------------- |
| 上游      | 上游      | ✅ 有       | 不存在      | 一切如常                                     |
| Syncdesic | 上游      | ✅ 有       | 不存在      | Syncdesic 无增强，退化为上游行为             |
| Syncdesic | Syncdesic | ✅ 有       | ✅ 有       | 两者合并，后者覆盖                           |
| Syncdesic | 上游      | ✅ 有       | ✅ 有       | 上游忽略 `.sdignore`（不识别），功能不受影响 |

`.sdignore` 不参与同步——与 `.stignore` 一样是 device-local 配置。Syncdesic 节点间如需共享 ignore 配置，可在 `.sdignore` 中用 `#include` 引用一个能同步的文件（复用上游已有机制）。

## 风险分析

`sdignore` parser 实现量：覆盖全部 gitignore pattern 格式约需 400-600 行 Go（若基于 `git-pkgs/gitignore` 封装，则仅需适配层约 100 行）。Content hash 解析约 200 行。`ContentMatcher` 结构约 150 行。

扫描阶段 content match 性能：content hash 匹配是 `map[[32]byte]bool` 查找——O(1)，无影响。

Puller 阶段 content match 性能：同上。

`.sdignore` 不存在时的行为：`ContentMatcher` 的 `contentHashes` 为空，`MatchContent` 始终返回 `NotIgnored`，零开销。

## 实现阶段

1. `lib/sdignore/` 包新建——实现 `.sdignore` 的 gitignore pattern 解析 + `?ch`/`?bh` parse
2. `lib/ignore/` 适配——`Matcher` 嵌入 `sdignore.Matcher`，改造 `Match` 方法
3. `ContentMatcher` 集成到扫描流程——在 `walk.go` 中追加 content match
4. Puller 集成——在 `need.go` 或 block request 层级过滤 content-ignored object
5. REST API 扩展——`/db/ignores` 返回 `.sdignore` 内容，GUI 中可编辑
6. 测试——`.sdignore` 解析测试、合并匹配测试、content ignore 端到端测试、上游兼容测试

---

此提案不改变 `.stignore` 的一切行为。上游节点无感知。Syncdesic 节点获得 content-based ignore 能力。

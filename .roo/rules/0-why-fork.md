# Syncdesic：当一块黑曜石板有了裂缝，我们选择自己磨一把刀

> Syncthing 的数据库层和协议层已经是 content-addressed 了。差的不是技术——差的是 BDFL 拒绝承认自己的架构已经活成了它声称反对的形状。

## 起因

Spacedrive 的梦想、分布式文件系统的承诺、IPFS 的愿景——Syncthing 已有 90% 的基础设施。卡在最后 10%，卡住的原因不是技术。

Synctrain（[pixelspark/sushitrain](https://github.com/pixelspark/sushitrain)，iOS 上的 Syncthing 客户端）由荷兰独立开发者 pixelspark（Tommy van der Vorst）从零搭建的原生 iOS/macOS 客户端，基于 MobiusSync iOS fork。他尽量 vanilla 使用 Syncthing 库，不 fork 上游——他是那个被 calmh 卡住的人。

## 解剖：calmh 的十年矛盾

> 他建造了一台 VFS 机器，然后说他不相信 VFS

Syncthing 的 `lib/fs` 包是装饰器模式的俄罗斯套娃：

```
caseFilesystem → mtimeFS → walkFilesystem → logFilesystem → metricsFS → BasicFilesystem
```

每层都是 VFS 包装。他写了一整台 VFS 机器。但他拒绝承认自己在做 VFS。

- 2024 年 PR `#9698`（FUSE loopback filesystem, cre4ture）——2 天内关闭，理由 "this is a feature we don't want, something I don't want to maintain"
- 同月 PR `#9691`（S3 block storage, cre4ture）——要求从新 folder 类型改为新 filesystem 类型，然后不了了之
- 2025 年 PR `#9887`（`custom` fsType, pixelspark）——pixelspark 自己提交的，为 Synctrain 暴露 iOS 相册 API。需求已经堆满门了，calmh 才挪一步
- 2026 年 PR `#10669`（`LayeredFilesystem` + `RemoteFilesystem`, calmh）——他自己写的。为了远程加载 ignore 文件。他合了一个字面意义的 VFS 层

结论：calmh 认为 "VFS" 是你拿来阻止别人 PR 的贬义词，不是架构分类。

### 性能瓶颈根因是 path-based 设计

issue `#7268`：casefs 注册表用单个互斥锁保护。数千个并发 block request 全阻塞在这个锁上，包括 API 请求（`/rest/db/status`）。goroutine 挂起 27 分钟。修复方案不是去掉锁——是再加一层缓存。

PR `#9839`：每个 incoming block request 重新创建 filesystem 对象。100 次 block 请求 = 100 次 filesystem 创建 = 100 次 `listdir` 调用。修复是缓存 filesystem 实例——掩盖了问题。

PR `#10454`：数据库需要分片才能处理大规模部署。folder DB 的 file-list 单表设计在大 block 数下写入性能崩溃。

PR `#10516`：流式扫描依赖路径排序来检测删除，但 SQLite 的 `ORDER BY name` 和 DFS 遍历顺序有微小差异。一个 rename 测试用例修了好几个来回，因为整个删除检测算法建立在"路径顺序一致"这个假设上。

### ignore 系统：十年不修的纪念碑

`#2491` 在 2015 年提出，标记为 v1.0 里程碑，v1.0 在 2019 年发布。十年过去，calmh 在 2025 年说

> "Developers may be familiar with git, but they are a tiny subset of Syncthing users."

这不是对用户的理解——是范式分歧。Git 用户想要 content-based ignore：不关心 file list，只关心内容。calmh 不觉得这种需求值得满足。

2023 年社区询问 content-defined chunking。calmh 回复 "limited utility"。但 Syncthing 的 block-level CAS 已经用了 SHA-256 寻址、`INSERT OR IGNORE` 去重、`filesblocklisthashonly` 索引。数据库层是 content-addressed 的，上层拒绝承认。

## 架构问题是什么

Syncthing 有一个优雅的核心：文件切分为 block，每个 block 通过 SHA-256 寻址，一组 block 的哈希列表再哈希一次得到 `BlocksHash`——本质就是 IPFS 的 CID。数据库有 `blocklisthash` 列，有 `INSERT OR IGNORE` 去重，有按 blocks hash 查询的索引。

数据库层和协议层已经是 content-addressed 了。调度层不是。

上游拉取管线（`pull` → `pullerIteration` → `processNeeded`）以 file list 遍历。`processNeeded()` 查询数据库返回文件路径列表，不是 block hash 图。同步调度循环的原子是文件，block 只在执行层出现。

这就是 `file-list based sync architecture`：调度器以文件为单位，然后才解析到 block。数据库按路径存 `FileInfo`（`name` key），`Need` 查询返回文件名，`pullBlock` 是管线末端执行层。

后果：

1. `selective sync 僵局`——上游 selective sync 本质是"file list 里没有此路径 = 不同步 + 允许删除"。你在一台设备上忽略路径，那个路径下的内容如果被其他地方引用，它仍会被删。因为没有 object 层，没有 refcount，没有"这个 content 还有用"的概念。

2. `流式访问不存在`——你要播放远端 4GB 视频，Syncthing 只能先同步整个 file entry。没有"给我这个 BlocksHash 下的第 3 个 block"这种按需请求。BEP 协议支持 block-level request，但上层的 file-list scheduler 拒绝暴露这个能力。pixelspark 被迫绕过上层直接调 `RequestGlobal`——他在 2024 年 7 月提交 PR `#9619` 将 `RequestGlobal` 变成公开符号，calmh 合了——但仅限于这个退路出口，架构问题没解决。

3. `去重只发生在 block 级别`——内容相同路径不同的文件，每个路径存一份 `FileInfo`。`BlocksEqual()` 是 content-level 的，但整个同步调度是 file-list-level 的。

## hash 偏执：为什么 calmh 不走 content-addressed

以上所有问题有一个共同的根因：calmh 不信任密码学 hash。他嘴上说 SHA-256 够安全，行动上把整个调度架构建在对 hash 的不信任之上。

hash 是内容的整合表示（`content(file) → 256 bits`），熵压缩到极限，没有冗余。这就是 Ω：系统的信息大于各部分之和，一个 hash 代表整个 block list 的全部信息。content-addressed 的核心信念：一旦你信任 hash 作为 identity，整个系统可以极致简洁——不需要 file list、不需要 path index、不需要冗余校验。hash is the source of truth，不是"用来校验的"，是"用来指代的"。

calmh 正好相反——他反 Ω。

`#582`（2014）：有人问为什么不能用 CRC32C 做 block hash，calmh 的回复暴露了他的基本立场：

> "We need this to be able to trust node IDs, and in the case of block hashes, to enable the optimization that if the block hash of two blocks is the same, we know that they have the same contents."

他完全理解 content-addressing 的逻辑。但同一句话里藏着矛盾：他一边说 hash 是用来信任的，一边在架构层面拒绝信任 hash。数据库有 `blocklisthash` 列，有 `INSERT OR IGNORE` 去重，有按 hash 查询的索引——但他不让调度层用。

`#2314`（2015-2016）：有人实现了完整可插拔 hash 架构（Murmur3 128bit，树莓派扫描快 4 倍），他关了：

> "I'm more and more convinced that we should not have it configurable at all and just make the right choice for everyone."

不是技术决策，是家长式统治。他不信任用户判断风险的能力。

`#6120`（2019）：XXH3 请求，他说了最直白的一句：

> "I don't want users to have to live with the probability of collisions and corrupting their own data, because someone on the internet told us to give it a shot."

对方说"我愿意承担风险"，他回复 "You are free to fork"。不是工程权衡——他在划清界限：我的项目，我说了算。

`#6373`（2020）：他自己写的 PR，注释里写：

> "Being paranoid and always recalculating the hash on put."

`paranoid` 是他自己写的。不是别人说的。

不信任 hash 的后果：他用冗余填补不信任留下的空隙。每轮 `pullerIteration`：

1. 全量查一次——但不信，hash 可能碰撞
2. 按 path 再查一次——但还不信，path 可能不对
3. 按 path 第三次查——差不多了，但 queue push 时不带 hash
4. pull block 时拿 weak hash 先过滤——adler32 也是 hash，但他不信
5. 再拿 SHA-256 校验——好，信了，只信这一块

结果是 `1 + 3N+` 次 SQL，每次 4-5 表 JOIN + protobuf 反序列化。数据库有 `blocklisthash` 索引，查一列 32 字节就能解决的问题，他非按 path 查三次、每次返回全量 protobuf。

ignore 系统同理。`(?cid)` 语法十年不做，因为 content-based ignore 意味着"我相信 block hash 能代表内容"——他做不到。

### 讽刺的终局

2024 年，PR `#9643`（Gusted）删掉了 `lib/sha256` 包。不是因为 calmh 终于接受 alternative hash——是 Go 标准库的 SHA-256 性能追上了 SIMD 实现。硬件加速追上了他的偏执。他当年拒绝换 hash 的理由之一"SHA-256 性能够好"在十年后被动成立，但他从未主动选择过这条路。

他不信 hash，但 hash 终究是 content 的唯一身份。他以冗余筑墙，墙内是 1+3N 次查询、queue 子系统、position-based blockDiff、temp+rename 物化——全是偏执的税收。

`Syncdesic 的第一行代码就是取消这笔税。`

## 范式分歧

calmh 眼中的 Syncthing：给普通用户在两台电脑之间同步文件夹。他做了正确的事：安全、可靠、易用。`lib/fs` 的装饰器架构对一个桌面文件同步工具足够好。性能瓶颈可以逐个修。ignore 系统支持 `!` 否定和 `(?i)` 已经够了。

我们眼中的 Syncthing：分布式的、content-addressed 的、P2P 数据传输协议。它已有 block-level 寻址、version vector 冲突解决、数据库层面 content dedup。它是一台跑车，被电子限速锁在 30km/h——因为 BDFL 拒绝承认那些性能问题和设计矛盾的根因是同一个：path-based 身份假设。

两个视角都不错。但它们不兼容。

### 先例教材：IPFS 为什么失败了

IPFS 的失败不是技术路线，是暴露层级的失败。

IPFS 把全局命名空间（Global DHT + 全局 CID 路由）强加给局部用户。每个 CID 对全网可见，每个节点参与 DHT 路由，内容寻址依赖全局一致的路由表。社会学上等价于公有制大锅饭：你只想用冰箱里的鸡蛋，但你必须维护整个农场的供应链。

个人用户设备间同步要求 AC，不是 AP。同一路由下的手机和笔记本之间没有物理分区延迟。用户需要的模型是"我的设备自动同步，不用我管"——这正是 Syncthing 做对了的地方：配对简单（无感部署 Pin node）、filesystem as interface，daemon 静默处理 CRDT 合并（用户理解 folder）。IPFS 要求用户理解 CID、DHT、pin 服务——用户不关心。

IPFS 把全局寻址暴露给了局部场景。理论上优美，工程上正确，用户体验上惨败。

Syncdesic 不会犯这个错误。

Syncdesic 的 ObjectID 是本地语义的——它只是你设备上某个 block 列表的 SHA-256。同一个内容在不同设备上有同一个哈希，但不要求全局路由表。device A 和 device B 之间通过 BEP 协议直接同步，不经过全局 DHT。离线时 ObjectID 解析仍然工作——本地数据库有完整 block 索引。网络恢复后 BEP 协议自动补全缺失 block。

Syncthing 的胜利范式——简单配对、folder 直觉、CRDT 自动合并——全部继承。我们只在上游基础上重构 sync pullup 流程，将 content-addressed object 抽象作为核心，不改变用户模型，不引入全局依赖。

### 我们打算怎么做

不从头实现。fork，修订 core + 加一层 object 抽象。

```go
// upstream syncthing
type Upstream struct {
    Path       string    // 一等公民
    BlocksHash []byte    // 衍生物
    Blocks     []Block   // 有效载荷
}

// 我们的视角
type Object struct {
    ID      [32]byte    // = BlocksHash，一等公民
    Paths   []string    // 属性之一
    Blocks  []Block     // 有效载荷
    PinRefs int         // 引用计数
}
```

关键设计：object 层透明，不破坏 BEP 兼容性。

上游设备发来 `IndexMessage{Name: "foo.txt", BlocksHash: H}`，我们收到后：

1. 按 path 写入 files 表（兼容上游 DB schema）
2. 按 BlocksHash 写入 object 表（新增）
3. 建立 path → ObjectID 映射
4. 上层问 `ignores.Match("foo.txt")` → 路径忽略了，但 ObjectID H 的 pin 不为 0，保留 blocks。
5. `vfs/` 层通过 symlink 让 `~ /objects/H/contents` 按需物化——不经过 file index，不依赖路径存在

上游设备发来 `Request{Hash: blockHash}`，我们回复 block。上游不知道我们的 fork 内部是 object-based 的。

BEP 协议不需要任何修改。我们的 fork 跟上游完全互通。

## 所以这个项目叫 Syncdesic

`Sync`（同步）+ `desic`（geodesic，测地线）——测地线同步。

测地线是两点之间在弯曲时空中的最短路径。在文件同步语境下，测地线就是"从 content 到需要这个 content 的设备之间的最短数据路径"。上游的 path-based 路由是绕弯的。我们要走测地线。

以及——`desic` 在拼写上也像 `desic-cate`（脱水保存）。我们把 Syncthing 的 core 脱水提纯，去掉那些阻碍 content 寻址的历史残留，让它变成它本应成为的样子。

## 最后

这不是对 calmh 的不尊重。Syncthing 是一个了不起的项目——它可能是 2010 年代最被低估的基础设施之一。BEP 协议的设计干净程度远超同期很多"专业"协议。calmh 在安全性和可靠性上的坚持值得尊敬。

但他的范式停留在了 2014 年：文件同步是路径匹配问题。十年后我们有一个不同的答案：文件同步是内容寻址问题。

他不打算改。我们不打算等。

---
title: "Syncdesic 操作手册"
version: "1.0"
last_updated: "2026-07-23"
---

## 定义

- REQ (Requirement): 必须严格遵守的强制性规则。
- CON (Constraint): 必须满足的限制或约束。
- GUD (Guideline): 推荐遵循的最佳实践或建议。
- PAT (Pattern): 特定情境下推荐使用的设计或实现模式。

## 项目结构

```
syncdesic/
├── ref/                    # 上游 syncthing submodule (v2.1.3-rc.1)
├── lib/                    # 核心库（与上游共享 import path）
├── internal/db/            # 数据库层（SQLite 后端）
├── cmd/                    # 可执行文件入口
├── .roo/rules/             # AI agent 规则
└── go.mod                  # module github.com/syncthing/syncthing
```

- GUD-001: `ref/` 是上游 submodule，**不得直接修改**。所有定制在 `ref/` 外实现。
- GUD-002: `go.mod` 应包含 `replace github.com/syncthing/syncthing => ./ref` 使 import 解析指向本地 submodule。
- GUD-003: `ref/` submodule 的 remote 指向 `github.com/syncthing/syncthing.git`，仅同步不做 push。

## 核心哲学

**Content-Addressed First**: 所有调度决策以 block hash 而非文件路径为第一身份。

1. GUD-010: 扩展 API 时优先考虑 content-addressed 接口，path-based 接口作为兼容层保留。
2. GUD-011: 新增查询优先查 `blocklist_hash`/`blocklisthash` 列，而非 `name` 列。
3. GUD-012: BEP 协议兼容性是硬约束——不得引入需要上游改协议的变更。

## 编码规范

1. REQ-001: 代码须实现高度自解释，严禁包含任何注释与 Docstring。
2. GUD-002: 采用 `.roo/rules` 文档驱动开发，规则先行。
3. REQ-003: 所有新增 public API 必须有对应的单元测试（TDD：先写测试再实现）。
4. REQ-004: 提交前运行 `go vet ./...` 和 `go test ./...` 确保无破坏。
5. REQ-005: `gofmt` 是唯一认可的代码格式标准。

## 测试规范

1. REQ-T001: 不准修改上游已有测试文件（`ref/` 及 `lib/` 中继承的 `_test.go`）。
2. REQ-T002: 新增测试放在与被测包同目录的独立 `_test.go` 文件中。
3. REQ-T003: 测试数据库使用 `:memory:` SQLite，不使用文件系统。
4. GUD-T001: 优先使用 mock/fake 连接而非真实网络。

## 设计约束

1. CON-001: 无必要不增实体（代码、函数、类或依赖）。
2. CON-002: 不改变 DB schema（新增表和索引除外，不得修改现有列定义）。
3. CON-003: 不破坏 BEP 协议兼容性——Syncdesic 节点必须能与原生 Syncthing 节点互通。
4. CON-004: 不引入外部存储引擎（SQLite 是唯一数据库后端）。

## 构建与测试

```powershell
# 构建
go build ./...

# 测试指定包
go test ./lib/model/... -v -count=1

# 全部测试
go test ./... 2>&1 | Select-String -Pattern 'FAIL|ok'

# 静态检查
go vet ./...
```

## 工作流协议

1. REQ-W001: **原子重塑** - 识别关键依赖链路边界，重构内部实现，确保对外 API 完全兼容。
2. REQ-W002: TDD 三阶段 - RED（写失败测试）→ GREEN（最小实现通过）→ REFACTOR（清理）。
3. SOP-W001: 操作前先确认 git 工作区干净（`git status`）。
4. SOP-W002: 文件存疑时使用 PowerShell 命令验证。
5. SOP-W003: 关键操作连续失败三次须暂停并征询意见。

## Git 规范

1. REQ-G001: commit author 使用 `Clawer 0x7E3 <noreply@github.com>`。
2. REQ-G002: 主分支名 `main`（与上游一致）。
3. REQ-G003: 功能分支命名 `feat/<描述>`，以 `/` 分隔。
4. GUD-G001: commit message 使用 `类型(范围): 描述` 格式。
5. GUD-G002: 推送前确保本地分支领先远程不超过 5 个 commit。

## 与上游的关系

1. GUD-U001: `ref/` submodule 固定到 tag，不跟踪 `main` 分支。
2. GUD-U002: 定期（无固定周期）从上游 fetch tag，评估是否更新 submodule。
3. GUD-U003: 上游修复的 bug 通过 cherry-pick 进入 `ref/`，不做合并。

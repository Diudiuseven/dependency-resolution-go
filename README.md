# depres - 确定性依赖版本解析工具

## 概述

`depres` 是一套确定性的依赖版本解析系统。多个模块各自声明依赖与版本范围，`depres` 将这些声明合并成一张依赖图，求解出唯一确定的版本组合。

### 核心特性

- **确定性求解**：相同输入下，无论模块顺序、并发程度或进程重启与否，得到的版本组合逐字段一致。
- **多版本并存**：同一依赖的多个不兼容大版本可以在结果中并存，传递依赖不跨大版本串味。
- **冲突可解释**：无法同时满足全部约束时，给出可读说明并指明矛盾来源。
- **异常可区分**：环、依赖缺失、非法版本范围等异常按可区分的原因失败，不会死循环或崩溃。
- **工作区支持**：多个成员各自持有清单并相互引用，求解跨成员进行；新增或移除成员不改变与它无关的既有结论。
- **锁定与校验**：把一次求解固化成可复现的锁定结果，支持安装与校验；清单与锁定结果不一致时识别缺项与漂移。
- **增量求解**：清单小幅变化后只重算受影响部分，增量结论与全量重算完全一致。
- **本地优先**：可用版本信息只来自本地 registry，不依赖外部网络。

## 求解语义

### 槽位模型

求解器将每个依赖映射到一个**槽位**（Slot）。槽位由 `(包名, 兼容边界)` 唯一标识。同一槽位内的所有约束必须由同一个版本满足。

兼容边界由兼容策略决定：

| 策略 | 边界 | 说明 |
|------|------|------|
| `major`（默认） | 大版本 | 同一大版本内共享一个版本（语义化版本兼容） |
| `minor` | 大版本.小版本 | 同一小版本内共享一个版本 |
| `exact` | 精确版本 | 每个版本独立槽位（不共享） |
| `unified` | 无边界 | 所有版本共享一个槽位（类似 pip 全局单版本） |

### 求解算法

求解器使用回溯算法（backtracking）：

1. 收集所有根依赖边，按依赖名排序。
2. 对每条依赖边，确定候选兼容边界（按最高版本降序）。
3. 对每个边界，选择满足所有约束的最高版本。
4. 递归求解该版本的传递依赖。
5. 若传递依赖求解失败，回溯尝试下一个版本或边界。
6. 所有边界都失败时，返回冲突错误。

### 确定性保证

- 依赖边按名称排序处理。
- 候选边界按最高版本降序排列。
- 版本按降序尝试。
- 结果槽位按 `(名称, 边界)` 排序输出。
- 不使用并发，避免并发导致的不确定性。

### 多版本并存

在 `major` 策略下，`foo@^1.0.0` 和 `foo@^2.0.0` 属于不同槽位（边界 1 和 2），可以并存。传递依赖也在各自的槽位中求解，不跨大版本串味。

### 冲突解释

当无法满足全部约束时，冲突错误包含：

- 每个约束的来源（哪个模块/包声明的）。
- 约束的具体内容。
- 矛盾来源汇总。

### 异常处理

| 异常类型 | 说明 |
|----------|------|
| `ConflictError` | 约束冲突，无法同时满足 |
| `CycleError` | 检测到依赖环 |
| `MissingDepError` | 依赖在 registry 中不存在 |
| `InvalidRangeError` | 版本范围格式非法 |
| `ResolutionError` | 其他求解错误（如取消） |

## 版本约束语法

| 语法 | 示例 | 说明 |
|------|------|------|
| 精确版本 | `1.2.3` | 仅匹配 1.2.3 |
| 插入号 | `^1.2.3` | >=1.2.3 <2.0.0 |
| 波浪号 | `~1.2.3` | >=1.2.3 <1.3.0 |
| 比较级 | `>=1.0.0 <2.0.0` | 合取（AND） |
| 通配符 | `1.x`, `1.2.x` | 匹配大版本或小版本 |
| 任意版本 | `*` | 匹配所有版本 |
| 析取 | `^1.0.0 \|\| ^2.0.0` | 析取（OR） |

### 插入号规则

- `^1.2.3` := `>=1.2.3 <2.0.0`
- `^0.2.3` := `>=0.2.3 <0.3.0`
- `^0.0.3` := `>=0.0.3 <0.0.4`

### 预发布版本

- 预发布版本（如 `1.0.0-beta.1`）小于正式版本。
- 预发布标识符按语义化版本规则比较（数字 < 字符串）。

## 工作区

工作区配置文件（`depres-workspace.json`）：

```json
{
  "members": ["app/package.json", "lib/package.json"]
}
```

成员清单可以使用 `workspace:*` 引用其他成员：

```json
{
  "name": "app",
  "dependencies": {
    "lib": "workspace:*",
    "foo": "^1.0.0"
  }
}
```

`workspace:*` 引用解析为成员的精确版本。工作区成员优先于本地 registry。

## 锁定文件

### 可复现性保证

锁定结果是对一次求解结论的纯函数，满足以下逐字节（byte-for-byte）保证：

- **不随运行时刻变化**：锁定文件不包含任何时间戳。历史版本曾写入
  `resolved_at`（生成时刻），该字段已在 `depres-lock/v2` 中移除。
- **不随机器/进程变化**：指纹与完整性哈希只对清单与 registry 的**内容**
  做 SHA-256，不读取路径、inode、文件修改时间等环境信息。
- **不随并发变化**：求解不使用会改变结果的并发；即使并发多次求解，
  每个槽位、每条边的写入顺序仍由名称/边界排序确定。
- **不随清单声明顺序变化**：依赖边在求解前统一排序，打乱清单中依赖
  声明的先后顺序得到相同锁定结果。
- **序列化确定**：所有数组字段（含空数组）固定输出为 `[]` 而非 `null`；
  槽位按 `(名称, 边界)` 升序、成员与边按名称升序写入。

因此，对相同的清单、本地可用版本信息与策略，连续两次求解、重启进程
后再次求解、或并发求解，写出的锁定结果必须逐字节一致。这一保证由
`TestLockByteReproducible`（跨秒）、`TestLockConcurrentReproducible`
（并发）、`TestLockShuffledManifestOrder`（声明顺序）以及
`TestCLILockReproducibleAcrossProcessRestarts`（独立子进程重启）覆盖。

### 文件格式（depres-lock/v2）

```json
{
  "format": "depres-lock/v2",
  "registry_fingerprint": "sha256:...",
  "policy": "major",
  "members": ["app"],
  "slots": [
    {
      "name": "foo",
      "boundary": "1",
      "version": "1.5.0",
      "integrity": "sha256:...",
      "edges": [
        {"name": "bar", "constraint": "^1.5.0"}
      ]
    }
  ],
  "unresolved_slots": [
    {"name": "e", "boundary": "1"}
  ]
}
```

字段说明：

- `slots`：最终**选定了具体版本**的槽位，含版本、完整性哈希与依赖边。
- `unresolved_slots`：求解结果中最终**没有落到具体版本**的槽位（例如求解
  过程中尝试过、随后因回溯被放弃的边界）。这些依赖不会被跳过或含糊带过，
  而是如实固化，使锁定结果完整对应一次求解结论。

### 说明输出（-explain）的内容范围

`depres resolve -explain` 与 `ExplainResolution` 输出：

- 每个槽位一行，按 `(包名, 兼容边界)` 升序排列，同一份结论多次输出
  逐字节一致；
- 已选定版本的槽位输出版本号与边界；
- 未选定版本的槽位输出“未选定具体版本（求解过程中尝试过，但未进入最终
  版本组合）”，不允许因这类槽位崩溃（历史版本对空槽位解引用版本号会
  panic，已修复）；
- 末尾“汇总”行给出槽位总数、已/未选定版本数量，并显式列出未选定版本
  的依赖及其边界。

### 旧锁定结果兼容策略

`depres-lock/v1`（含 `resolved_at`、无 `unresolved_slots`）仍可继续
读取、校验（verify）与安装（install），现有项目不会因字段调整失效：

- 读取时做内存模型转换，`resolved_at` 被忽略且不参与任何复现性判断；
- 同时在 stderr 输出**可区分的兼容提示**，类别包括：

  | 提示类别 | 含义 |
  |----------|------|
  | `legacy-v1-format` | 文件整体为旧的 depres-lock/v1 格式 |
  | `resolved-at-removed` | 遇到已移除的 `resolved_at` 字段，已忽略 |
  | `unknown-field` | v2 文件中存在当前实现不认识的字段，已忽略 |

- 代码层面通过 `ReadLockWithNotices` 取得 `[]CompatNotice`；`ReadLock`
  保持原签名（内部等价调用，忽略提示）。
- 建议在下次成功求解后重新写回为 `depres-lock/v2`（`verify -policy update`
  或重新 `resolve` 即可）。

验证方法：

- 单元测试 `TestLegacyV1LockCompatible` 用内联的 v1 文件断言：可读取、
  提示类别齐全、`Verify` 无缺项/漂移、`Install` 成功；
  `TestLegacyV1UnknownFieldReported` 断言未知字段被报告且不影响读取。
- 手工验证：对一份 `depres-lock/v1` 锁定文件执行
  `depres install -lock <file>` 或 `depres verify -lock <file>`，
  应在 stderr 看到 `兼容提示[...]`，命令退出码为 0。

### 校验策略

| 策略 | 说明 |
|------|------|
| `strict` | 任何不一致都报错（默认） |
| `warn` | 报告不一致但继续 |
| `update` | 重新求解并更新锁定文件 |

### 缺项与漂移

- **缺项**：清单中有依赖边，但锁定文件中没有满足的槽位。
- **漂移**：锁定文件中的版本与 registry 不一致（内容变更或版本移除）。

## 增量求解

增量求解器使用缓存加速：

- 缓存键：`(包名, 兼容边界, 排序后的约束集合)`。
- 当槽位的约束集合不变时，复用缓存的版本和依赖边。
- 当约束集合变化时，重新求解该槽位。
- 增量结论与全量重算完全一致（通过测试验证）。

### 取消与超时

- 求解器支持 `context.Context`，可随时取消。
- 取消后不写入锁定文件（原子写入：先写临时文件再重命名）。

## CLI 使用

### 求解

```bash
depres resolve -registry ./registry -manifest app.json -explain
```

### 安装

```bash
depres install -registry ./registry -lock depres-lock.json -target node_modules
```

### 校验

```bash
depres verify -registry ./registry -manifest app.json -lock depres-lock.json -policy strict
```

## 测试

```bash
go test ./resolver/ -v
```

测试覆盖：

- 确定性（模块顺序无关）
- 冲突与解释
- 多版本并存
- 工作区（新增/移除成员）
- 锁定校验（缺项与漂移）
- 增量求解（与全量一致）
- 异常输入（环、缺失、非法范围）
- 兼容策略可配置性
- 钻石依赖
- 大规模确定性
- 说明输出对回溯残留的未选定槽位不崩溃，且完整可读（`TestExplainAbandonedSlotDoesNotCrash`、`TestCLIExplainDoesNotCrash`）
- 说明输出确定性（`TestExplainDeterministic`）
- 锁定结果逐字节一致：跨秒连续运行、并发、清单顺序打乱（`TestLockByteReproducible`、`TestLockConcurrentReproducible`、`TestLockShuffledManifestOrder`）
- 锁定结果跨进程重启逐字节一致（`TestCLILockReproducibleAcrossProcessRestarts`）
- 旧 `depres-lock/v1` 锁定结果的兼容读取、校验、安装与提示（`TestLegacyV1LockCompatible`、`TestLegacyV1UnknownFieldReported`）

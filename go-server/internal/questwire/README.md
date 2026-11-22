# 进城任务手册

首次加载城镇时，服务端读取所选角色的任务、普通背包和已提交的奖励收据。依次发送 N561 活动目标与剩余量、N21 可接任务和 N356 已清任务。N356 将 30,000 个标记用原服使用的 zlib-ng 实现压缩。

资源导出器从匹配版本 PVF 读取 2,905 个普通任务，以及装备图鉴和 DPL 任务券涉及的 16 个任务条目。职业、转职、等级、位置、已清前置、互斥选择及来源条件都会影响手册。材料任务只统计普通背包。含有进度的旧存档若缺少匹配的目标定义，投影会返回角色对应的任务号。城镇到达确认 C33 依赖已提交的到达收据。任务完成、扣除交付物、发奖及关联子任务属于事务命令。[资源导出器](../../tools/catalog-export/QuestExport.cs)

在持有匹配 PVF 的项目内，从 `go-server` 目录运行下列命令。测试比较 76 个建角模板在两个地点的可接清单，并用原 C# 的八组 1、30、50、51、75 和 100 级存档逐字节核对三种通知。样本包含非空狩猎与材料任务、不同答案的已清标记及 N356 压缩正文。[C# 样本生成程序](testdata/Program.cs)

```powershell
.\tools\export-characters.ps1
.\tools\go.ps1 test -count=1 -v ./internal/questwire
```

## 城镇接取与放弃

普通城镇准备完成后，C31 接取、C32 放弃都支持原始 2 字节任务号和带操作号前缀的 4 字节请求。C31 首次接取发送 ACK 和 N561、N21、N356。重复接取一个已活动任务时，同一角色会话先收到一次 ACK，之后静默。重选角色或重连会重新建立这项回包记录。C32 检查 PVF 的可放弃标志和活动子任务；放弃后再放弃，返回原错误正文 `00 07 00`。

当前 PVF 的 2,905 条普通任务中，2,874 条带可放弃标志，7 条使用每日周期。日切为 UTC+8 的 06:00。唯一接取时发物品的任务 5654 需要一份模板 4365。缺少时，道具和任务在同一数据库事务写入，旧服用 N14 的普通背包增量标记新道具。已有时 C31 返回现有槽号及绝对数量。放弃任务保留物品，再接取沿用同一实例。完整 Host 启动时逐条验证操作规则和接取物品模板；早期导出的 schema 4 目录缺少这些字段时，错误会提示重跑 `tools/export-characters.ps1`。

23 个旧服原始请求及其 64 个应答通过真实 TLS 网关在 PostgreSQL 副本上重放。逐包比较覆盖正常接取、重连、相同序号重发、准备完成前的拒绝、活动子任务阻止放弃、PVF 禁止放弃、未知任务及错误前缀。独立字段测试另检查请求长度。每次请求都对照角色任务 JSON、版本、物品属性、模板和 119 字节冻结核心。样本以独立的 v35 城镇角色为底本，测试设置为 100 级并预置 3248、5653、6211 三个已清前置，角色从版本 104 推进至 115。[旧 C# 原始记录](testdata/legacy-commands.json.gz)

以下命令在已有城镇测试存档及匹配 PVF 的 `go-server` 目录运行。每次从同一存档导入一个新测试库；将示例库名换成项目内尚未使用的名字。C33 目标触发、C34 扣物和发奖、决斗大厅与地下城的任务命令仍需相应场景处理。[实服验收程序](testdata/commands.py)

```powershell
.\tools\go.ps1 install ./cmd/serve100 ./cmd/multiplayer-admin
python -B .\internal\questwire\testdata\commands.py --prepare
$fixture = 'quest_cmd_' + (Get-Date -Format 'yyyyMMdd_HHmmss')
.\tools\go.ps1 run ./tools/postgres/importfixture -name $fixture -source ./.work/quest-commands/v35-town-quests.sqlite3 -account quest-wire-v35
$pgBin = Split-Path (Get-Process postgres | Select-Object -First 1 -ExpandProperty Path)
python -B .\internal\questwire\testdata\commands.py --dsn-file (Join-Path ..\.work\postgres-storage ($fixture+'-dsn.txt')) --libpq (Join-Path $pgBin 'libpq.dll')
```

# 进城库存协议对照

`internal/inventorywire.Town` 从已选角色的 PostgreSQL 库存快照和该角色的 Gameplay 生成两个有序通知批次。第一批对应旧服 `LocalAssetsModule.GameplayPackets`，第二批对应 `LocalSpecialAssetsModule.BuildProjection`。调用方在旧服的两个投影位置用当次请求的 sequence 封帧。活动容器通过 `FateContainers` 在 Fate 模块原来的位置发送。

| 位置 | 线上内容 |
| --- | --- |
| 普通库存 | N13 列表 0 含金币、复活币与普通包。N14 更新 15 个装备槽。N53 更新账号点券 |
| 普通库存的条件项 | 有普通宠物消耗品时先同步列表 7。限购 N851。开通的个人仓库 2 和账号仓库 12。有物品的共用栏 45、勋章栏 38 |
| 特殊库存 | 时装栏 1、宠物和神器栏 7、N105 宠物序列、N14 已穿神器、宠物和时装。已解锁宠物外观时保留槽 33 |
| Fate 首次进城 | 列表 44。如果普通批次未投影非空列表 45，此处清空 45。已存在的物品保留完整原始核心和槽位 |

已穿普通装备使用 119 字节物品格式。空槽写入 `FFFFFFFF` 模板标记。时装另含插槽和颜色块，宠物另含自身序列号、存续时间和 GB18030 名称。失败的投影返回错误，调用方可以在发第一帧前终止该批次。

`Options.CashPolicies` 接受匹配 PVF 提取的账号和角色购买限制，`AccountPurchases` 接受账号已有计数。角色计数读取 Gameplay，投影按游戏 UTC+8 的日、周、月或终身规则计算下一刷新时间。宠物默认名由可选的 `PetDefaultName` 资源查询提供。旧库导入能否启用带特殊资产或既有购买流水的存档，另由存储模块的校验决定。

两条来自真实 C# TCP 的回放同时覆盖教学开场和已跳过教学后的首次进城，默认新号有 10 复活币、普通背包扩展 0、个人仓库 152 格。每条回放比对 11 个连续常规和特殊资产通知以及 2 个 Fate 容器。回放中的 N851 携带 19 行限制。测试用原始正文检查插入位置，并用单独的日、周、月、终身和旧周四记录测试校验计算。[原始回放](../protocol/legacy-town.json.gz)

`nonempty.json` 用原 `Serve100.Protocol.dll` 的公开编码方法导出。样本包含已穿武器、带不透明字节的物品、两种仓库、宠物消耗品、时装后缀、宠物序列和 GB18030 名称、神器、勋章及活动物品。Go 逐字节与原编码结果比较。[C# 导出脚本](Export-Vectors.ps1)

```powershell
.\tools\go.ps1 test ./internal/inventorywire -count=1
.\testdata\inventory\Export-Vectors.ps1 -ProtocolAssembly ..\baseline\.work\source\Server\artifacts\bin\Serve100.Host\release\Serve100.Protocol.dll
```

城镇 C19 的两份原始 TCP 记录使用相同来源的 v35 存档。模板 27302 从穿戴栏 12 移入普通背包 9、穿回并重放同一序号。模板 1085 从槽 65 的 8 件拆出 3 件到槽 67，再合并入槽 66，拆分与合并各重放一次，最后恢复初始数量。Go Host 经真实 TLS 和 PostgreSQL 返回的 15 个武器应答帧与 20 个堆叠应答帧连同帧头逐字节吻合。断线重新选角后，武器仍在栏 12，槽 65、66 分别有 8、4 件。普通 C19 直接生成 N13 背包与 N14 穿戴，随后按原条件发送 N2 角色信息和武器外观。[武器原帧](legacy-weapon-moves.json.gz) [堆叠原帧](legacy-stack-moves.json.gz)

独立导入测试另外验证 v35 原库存中的一件武器全部 119 字节、19 金币、10 复活币和 51 点券。以下命令使用项目内已准备的三个专用测试库和匹配资源目录。

```powershell
$env:S9A1_INVENTORY_FIXTURE_DSN_FILE = (Resolve-Path ..\.work\postgres-storage\equipped-dsn.txt).Path
.\tools\go.ps1 test -count=1 -run TestImportedOrdinaryWeapon -v ./internal/inventorywire
.\tools\go.ps1 install ./cmd/serve100 ./cmd/multiplayer-admin
python -B .\internal\mailwire\testdata\live.py weapon
python -B .\internal\mailwire\testdata\live.py stack
```

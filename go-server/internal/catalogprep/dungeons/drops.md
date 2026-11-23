# 从 PVF 准备普通掉落

`BuildDrops` 组合普通地面掉落、怪物个别掉落、独立和分组怪物卡、额外怪物及物件池、通关资源、幸运、装备生成和首猎参数。额外怪物与物件池使用各自完整的等级窗口，从同一批原始候选中筛选。附在其他所有者上的次级资源仍进入原版的资源哈希链。

匹配项目 PVF 的独立导出与 C# 目录逐字段一致。比对覆盖 24,829 条源候选、9,107 条进入导出的候选、503 个普通池、2,270 个额外怪物和物件池、11,028 条可验证携带重量、406 个怪物与 40 组通关资源。两条带重复 `[impossible contents]` 的零权重装备保留原解析拒绝。独立掉落和怪物卡记录按原所有者与编号核对。

普通掉落资源哈希为 `6D2FB0B904E32EB704C821B235E1111D19B158B7D2EB435C54A6DF650DFFACED`。来源见 [旧掉落导出](../../../tools/catalog-export/DungeonDropExport.cs) 和 [旧普通掉落目录](../../../../s9a1/Server/src/Serve100.Infrastructure/Resources/LocalDungeonDropCatalog.cs)。

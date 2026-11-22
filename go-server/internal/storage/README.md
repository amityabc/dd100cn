# PostgreSQL 存档和旧服导入

游戏进程通过 pgx 连接 PostgreSQL。账号行锁串行化同一账号的命令，连接获得的 owner UUID 和 epoch 共同标识会话。角色 ID 使用旧协议的 1～65535，选角请求使用按存储槽和角色 ID 排列后的显示下标。断线重连会替换旧 owner，旧连接不能再写角色，也不能把新会话登出。

## 数据范围

- 角色列表保留 ID、槽位、名字、职业、等级、创建时间、版本、属性、gameplay、出生资源哈希、城镇坐标和房间出口。账号保存 372 字节设置和原始快捷键。正常建角沿用旧服初始钱包和冻结职业模板。
- 普通资产保存实例 UID、完整模板、武器资格、数量、槽位、属性、119 字节核心和 detail。背包与仓库读取同时校验角色和所属账号。选角和名册从穿戴实例重建原生槽。普通货币和四种限时账号契约均按旧档宽度读取，契约以绝对 Unix 秒保存。
- 副手槽24按女鬼剑士职业、成长分支、技能95/251和光剑冻结资格判断。旧武器列缺少资格时会采用同实例完整模板内的已验证资格。穿戴属性中的品质、耐久应与武器列一致。
- 普通城镇首次 C120 或 C143 的背包投影前，按旧顺序整理角色背包、个人仓和账号仓里对应 12 种共享材料。空固定格 363～374 保留第一个实例的 UID，兼容堆叠保留目标 UID、模板和原始核心，完整来源记录写入角色的 `SharedMaterialImports`。锁定、带不相同属性或核心、合计超上限的材料保留原位。有变化时角色版本和账号资产版本各增一次。`testdata/legacy-shared-material.json` 是旧 C# 的 `Import` 和 `ValidateImports` 都已校验的输入、结果和完整历史。
- 旧服进城的下一步还会对已到期的每日删除道具比对当期 PVF 完整模板，命中后退休 UID 并写到期收据。当前 Go 版尚待接入这段流程，含此类道具的旧档仍需对照。
- 历史奖励收据按角色和业务键保存原始完整 JSON，含输入、结果、版本与授予实例。教学跳过核验职业、副本、区分大小写的布局哈希和回归点，更新角色版本和位置。完成教学的角色按城镇目录识别登录房间，重登同房时保留坐标。
- 251、252 决斗频道首次同步会在已选角色下保存独立的 `DuelSkills` 技能页。普通技能、剩余 SP/TP 和已有的指令锁由原始技能规划规则复制，数据库只更新这一页并增加一次角色版本。重连及重复同步保留既有决斗页、城镇坐标和选角阶段。
- 新手自动入场按 UTC+8 的 06:00 计算疲劳日，当前选择的重复入场只回放，重登未完成教学会重新开启副本。普通教学 C39 在一笔事务中保存角色经验、等级、完整进度和原始击杀收据；原连接重报同一击杀不增加经验。末房完成教学与返城沿用旧服单独事务。
- C36 和 C1418 同时保存位置、角色版本、到达任务和需要记录的完整室外点。进房时取数据库中最后一次已提交的坐标；已离房重发 C1418 时，按角色所在区域判定重放。玩家在该区域内走动不会丢掉上一跳的回放，也不会把当前位置重置到旧落点。重选角色时清除上一跳记录。
- 普通背包与穿戴使用 C19 的 28 字节原始请求。角色版本、两格模板和冻结资格通过后才写入，交换保留 UID 和完整 opaque 数据。拆分产生新 UID，合堆保留目标 UID。相同会话只有上一笔 sequence、完整 body 以及角色和共享资产版本都匹配时才回放。向背包增加某一模板数量需使用配套 PVF 的重量。C19 的普适重放与独立的 C20 排列命令具有各自的用途。
- 普通城镇 N1734 取当日收据以及关联的免费翻牌原记录。要求已领次数连续、免费卡与计数原记录相同、真实授予的数量和 UID 相符，Ghent 离线加次还检查操作 ID 和版本顺序。已经用旧服实际规则生成并核验金币免费卡、普通永久装备和材料、装备幸运快照、特殊卡与通关奖励的合并授予收据。当前尚未支持的定时、强化生成、神话或宠物神器等历史卡片会返回具体错误。

## 导入

从 `go-server` 目录运行。先使用 SQLite 一致备份或已正常关停的旧库。命令以只读连接开始同一个 SQLite 快照。在线使用 WAL 的旧库应通过 SQLite backup API 取出文件。

```powershell
.\tools\go.ps1 build -o .\bin\import-save.exe ./cmd/import-save
.\bin\import-save.exe -mode inspect -source $oldSave
.\bin\import-save.exe -mode stage -source $oldSave -account $account
.\bin\import-save.exe -mode activate -source $oldSave -account $account
```

stage 和 activate 通过环境变量 `S9A1_DATABASE_URL` 指定目标数据库。inspect 仅输出报告。stage 在 `s9a1_import` 保存整份建表定义、表行及每张表的 SHA-256，区分 NULL、整数、浮点、TEXT 和 BLOB，PostgreSQL 读回核验通过才提交。activate 单独开启事务，校验原名、原 ID 和目标账号，写入可供游戏读取的数据。观察 `Staged`、`Activated` 和 `ActivationIssues` 可以区分两阶段结果。同一份快照和同一目标重复执行可安全重试。既有账号或与其他玩家冲突的名字、ID、资产 UID 会使激活回滚。

只读归档识别 local-entry v1～v35。激活当前使用旧服官方迁移后的 v35。非空装扮或宠物、待改名角色、未接入的活动或其他表会列出原因并留在暂存阶段。多份独立旧服存档出现相同角色 ID 时，需要先决定账号映射规则，否则无法在同一服保持两个原 ID。

## PostgreSQL 备份与恢复

从 `go-server` 目录执行以下命令。使用 Python 3.11 或更高版本、与数据库同主版本的 PostgreSQL 客户端工具。`$dsnFile` 是只供本机读取的 PostgreSQL URI 文件。已配置 `S9A1_POSTGRES_DSN` 或 libpq 服务时可省略 `--dsn-file`。工具可经 `--bin-dir` 指定，本项目已验证的 Windows 17.11 客户端在 `.tools/pg17-client/bin`。[PostgreSQL 官方 Windows 发行入口](https://www.postgresql.org/download/windows/)

```powershell
python -B tools/postgres/save_archive.py backup --dsn-file $dsnFile --archive .work/backups/before-cutover
python -B tools/postgres/save_archive.py restore --dsn-file $dsnFile --archive .work/backups/before-cutover --new-database s9a1_restored
```

备份用 `pg_dump` 的一致性快照保存 `s9a1` 游戏数据和已存在的 `s9a1_import` 旧档暂存，一并生成 SHA-256。恢复先核对摘要，再按源库的编码与排序规则创建新库，通过 `pg_restore` 在单个事务内装入。源库和已有目标库可继续使用。数据库角色与授权、游戏网关的签发文件分别归运维管理。正式切换时先结束旧库写入，再取最后一次快照。[pg_dump](https://www.postgresql.org/docs/17/app-pgdump.html) · [pg_restore](https://www.postgresql.org/docs/17/app-pgrestore.html)

在独立 PostgreSQL 17.11 上已完成恢复演练。13 表21行逐表内容、105列、21个索引、97个约束及身份序列状态一致。原装备 UID、119 字节、角色 ID 1、26 金币、51 Cera、一次 +7 的幂等账本、含最大 uint64 的旧收据和 v35 暂存档均已单独核对。尝试覆盖目标、覆盖备份或恢复已损坏的备份均失败，未写入已有库。演练报告位于 `.work/postgres-backup/reconciliation.json`。

## 验证

`tools/postgres/Test-Instance.ps1` 用已有的 PostgreSQL 二进制建立只监听回环的隔离实例。当前数据库用 PostgreSQL 17.11 验证，普通交换使用 PostgreSQL 15 引入的 NULLS NOT DISTINCT 约束。通过 `S9A1_PG_BIN` 或 `-BinDirectory` 指定其 bin 目录，数据、日志和本机连接文件均在项目根的 `.work/postgres-storage`。从 go-server 目录运行下列命令。

```powershell
.\tools\postgres\Test-Instance.ps1
$env:S9A1_TEST_DATABASE_URL = Get-Content -Raw ..\.work\postgres-storage\test-dsn.txt
.\tools\go.ps1 test -count=1 ./internal/storage/... ./internal/rewardcount ./internal/inventorymove
```

原生 C5 通过旧 C# 服务端和客户端 Script.pvf 创建的 v35 样本含一名角色、10 复活币、0 金币、0 Cera、空穿戴和空奖励收据。在该库的独立副本上应用 `legacy/testdata/v35-ordinary-fixture.sql` 可加入上游 LocalWeaponTests 的武器向量、钱包、历史收据和四种不同的契约截止时间。将该副本激活到独立数据库的 `ordinary-v35` 账号，设置 `S9A1_IMPORTED_TEST_DATABASE_URL` 后运行上述测试，会核对名册和选角返回的 slot12、原始 119 字节、UID 和历史收据的无符号 64 位数。

首次入场可能生成新技能页或迁移旧道具。需要重复验证首次入场时，可从同一份 SQLite 备份创建新的项目测试数据库。`-name` 每次选一个新库名，角色和物品 UID 按原值导入，命令输出新库的本地连接文件路径。

```powershell
.\tools\go.ps1 run ./tools/postgres/importfixture -name session02 -source $oldSave -account $account
```

供 C19 和房间联测的 `v35-town-equipped.sqlite3` 另取自旧服已过教学并实际走动的角色。隔离用例使用原 C# 进阶目录将角色从 1 级升至 5 级，使用配套 Script.pvf 中模板 27302 的冻结资格、45 耐久和 4000 重量，将固定测试 UID 放入穿戴槽12。该角色在 38/1 房间，打开普通包格9并移动该装备即可观察完整穿脱。

同一角色的独立 `v35-town-stacks.sqlite3` 副本另存有两个模板 1085 的普通永久堆叠。格65原有8个，格66有4个。向空格67拆出2个，再合入格66，应留下格65与66各6个，两个原 UID 保持，格67的新 UID 在全部合并时退休。两份样本分别导入不同测试数据库。

`v35-town-shared.sqlite3` 在该城镇角色的独立副本上，使用旧 C# 读取配套 Script.pvf 的模板 3033，加入背包4个、个人仓5个、账号仓6个及另外10个锁定材料。独立旧 C# Host 和 Go Host 对同一快照执行首次 C120，11 个 N13/N14 整帧逐字节一致，合并帧 SHA-256 为 `962a81a881120551938f0f009c01aca03de1123c7ba8221627ebb53b93830a94`。两库的来源历史、角色版本105和账号资产版本1均一致。记录在 `testdata/legacy-shared-town.json.gz`。

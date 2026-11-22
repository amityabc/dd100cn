# PostgreSQL 存档和旧服导入

游戏进程通过 pgx 连接 PostgreSQL。账号行锁串行化同一账号的命令，连接获得的 owner UUID 和 epoch 共同标识会话。角色 ID 使用旧协议的 1～65535，选角请求使用按存储槽和角色 ID 排列后的显示下标。断线重连会替换旧 owner，旧连接不能再写角色，也不能把新会话登出。

## 数据范围

- 角色列表保留 ID、槽位、名字、职业、等级、创建时间、版本、属性、gameplay、出生资源哈希、城镇坐标和房间出口。账号保存 372 字节设置和原始快捷键。正常建角沿用旧服初始钱包和冻结职业模板。
- 普通资产保存实例 UID、完整模板、武器资格、数量、槽位、属性、119 字节核心和 detail。背包与仓库读取同时校验角色和所属账号。选角和名册从穿戴实例重建原生槽。普通货币和四种限时账号契约均按旧档宽度读取，契约以绝对 Unix 秒保存。
- 副手槽24按女鬼剑士职业、成长分支、技能95/251和光剑冻结资格判断。旧武器列缺少资格时会采用同实例完整模板内的已验证资格。穿戴属性中的品质、耐久应与武器列一致。
- 普通城镇首次 C120 或 C143 的背包投影前，按旧顺序整理角色背包、个人仓和账号仓里对应 12 种共享材料。空固定格 363～374 保留第一个实例的 UID，兼容堆叠保留目标 UID、模板和原始核心，完整来源记录写入角色的 `SharedMaterialImports`。锁定、带不相同属性或核心、合计超上限的材料保留原位。有变化时角色版本和账号资产版本各增一次。`testdata/legacy-shared-material.json` 是旧 C# 的 `Import` 和 `ValidateImports` 都已校验的输入、结果和完整历史。
- 普通城镇及洞察首次进场先整理共享材料，再核对背包、个人仓、账号仓的每日删除堆叠。只有已到保存期限、实例状态完整、能匹配当期 PVF 模板的物品才会退休。角色修订和带 UID、原 119 字节、原数量、检查时间的收据在同一 PostgreSQL 事务提交。涉及共用仓位时更新账号资产版本。[进城到期处理](inventory_expiry.go)
- 历史奖励收据按角色和业务键保存原始完整 JSON，含输入、结果、版本与授予实例。教学跳过核验职业、副本、区分大小写的布局哈希和回归点，更新角色版本和位置。完成教学的角色按城镇目录识别登录房间，重登同房时保留坐标。
- C46 冻结旧服格式的免费、付费、特殊和通关奖励计划，保留计划中的物品属性、核心、详情及装备幸运资格。C71 在同一事务校验会话、原计划、正式选择包、消费与实际授予后提交收据。付费结果记录价格和计划物品数，特殊奖励的每日计数与免费牌收据共享实际授予记录。旧 Go 冻结计划保留原 JSON 作为领取凭据。洞察的每周奖励另核对入场与通关证明、周序、免费卡和登记收据，计数查询读取当前选中角色。
- 智慧的引导 C15 只读预检。C16 按 PVF 顺序优先扣除有效通行证，否则按背包格顺序扣足 18 个时间引导石。材料、疲劳和 `wisdom-entry` 收据同事务提交。收据冻结角色会话、原布局 SHA-256、变体、难度、随机种子、奖励计划和扣除前的完整道具实例。C16 重放核验收据后只回 ACK。C46、C69、C70 核验已提交的房间击杀后投影无翻牌结果。C42、C72 返城时保留这些历史收据。
- C64 按整个购物车结算点券和金币。探险记进阶证及豪华包由配套导出目录提供商品、价格和季节指纹，权益随购物车同事务提交。已持有进阶证的账号仍可买豪华包。豪华包要求低于 50 级，成交将账号探险记等级推进到 50，经验归零并保留既有领奖记录。
- 游戏内 C176 普通 GM 发放以一次服务端操作 UUID 冻结装备品质，保留完整授予收据和实际实例数量。装扮、宠物与宠物装备走专用实例工厂及审计。独立 GM 索引复用匹配 PVF、客户端档案及解析器版本的缓存，原 GUI 的 JSON 字段、运行参数和默认缓存位置沿用旧服约定。
- C507 action65 只转换匹配资源明确列出的永久装扮，要求同部位、同品级，使用事务内的已选角色和完整原券。转换保留实例 UID、详情 UID、装备核心、属性、镶嵌和颜色。克隆转为实际装扮时清除已经复制的外观。确认后先发送 ACK507，再刷新普通与装扮背包。即时重放只同步资产并释放原客户端待处理状态。
- 251、252 决斗频道首次同步会在已选角色下保存独立的 `DuelSkills` 技能页。普通技能、剩余 SP/TP 和已有的指令锁由原始技能规划规则复制，数据库只更新这一页并增加一次角色版本。重连及重复同步保留既有决斗页、城镇坐标和选角阶段。
- 新手自动入场按 UTC+8 的 06:00 计算疲劳日，当前选择的重复入场只回放，重登未完成教学会重新开启副本。普通教学 C39 在一笔事务中保存角色经验、等级、完整进度和原始击杀收据；原连接重报同一击杀不增加经验。末房完成教学与返城沿用旧服单独事务。
- C36 和 C1418 同时保存位置、角色版本、到达任务和需要记录的完整室外点。进房时取数据库中最后一次已提交的坐标；已离房重发 C1418 时，按角色所在区域判定重放。玩家在该区域内走动不会丢掉上一跳的回放，也不会把当前位置重置到旧落点。重选角色时清除上一跳记录。
- 普通背包与穿戴使用 C19 的 28 字节原始请求。角色版本、两格模板和冻结资格通过后才写入，交换保留 UID 和完整 opaque 数据。拆分产生新 UID，合堆保留目标 UID。相同会话只有上一笔 sequence、完整 body 以及角色和共享资产版本都匹配时才回放。向背包增加某一模板数量需使用配套 PVF 的重量。C19 的普适重放与独立的 C20 排列命令具有各自的用途。
- 普通城镇 N1734 核验当日计数与关联的免费翻牌收据，检查连续次数、原始计划、实际授予数量及 UID。根特离线加次还检查操作 ID 和版本顺序。历史免费卡支持旧服允许的有时限 PVF 物品及宠物装备。特殊卡和通关奖励按旧服完整物品规则核验生成强化、初始增幅和神话词条。旧付费卡随原收据迁入，原连接结束后其副本结算不再重开。
- 命运的抉择以 C1919 提交已生成布局的节点，C39、C1896、C1929 和 C1930 仅上报，C45 回旧服的非活动门拒绝。N2055 同步点数、祝福、罪业、收藏装备和按周解锁的故事状态。C1894 与返回结算在同一事务记录 UTC+8 06:00 划分的每日阶段奖励与邮件。C160 阶段箱和兑换箱保留原物品实例、数量、目标容器和实际授予凭据。阶段箱写账号共同背包，兑换箱写角色背包。
- C40 保存旧服格式的房间、死亡轮次、坐标和关联角色。C41 在持久化事务中核对同一份死亡输入，并一次提交扣币、资源恢复和冷却清除。洞察续进保留前代全部怪物和被动对象的凭据，从原布局和前代已击败命名怪的稳定来源索引派生当前指纹。预算从原始币上限、各代命名怪奖励及成对的死亡和复活收据计算。C48 使用当前实际场景的地图 ID 判定耐久重报。

## 导入

从 `go-server` 目录运行，源可以是旧 SQLite 数据库或它的一致性备份。命令以只读事务固定同一份数据库视图，包含事务开始前已提交的 WAL 内容。旧库索引、触发器、表数据和摘要均从该视图读取。

```powershell
.\tools\go.ps1 build -o .\bin\import-save.exe ./cmd/import-save
.\bin\import-save.exe -mode inspect -source $oldSave
.\bin\import-save.exe -mode stage -source $oldSave -account $account
.\bin\import-save.exe -mode activate -source $oldSave -account $account
```

stage 和 activate 通过环境变量 `S9A1_DATABASE_URL` 指定目标数据库。inspect 仅输出报告。stage 在 `s9a1_import` 保存整份建表定义、表行及每张表的 SHA-256，区分 NULL、整数、浮点、TEXT 和 BLOB，PostgreSQL 读回核验通过才提交。activate 单独开启事务，校验原名、原 ID 和目标账号，写入可供游戏读取的数据。观察 `Staged`、`Activated` 和 `ActivationIssues` 可以区分两阶段结果。同一份快照和同一目标重复执行可安全重试。既有账号或与其他玩家冲突的名字、ID、资产 UID 会使激活回滚。

local-entry v35 直接走激活校验。local-entry v1～v34，含旧服可识别的无版本标记 v1～v3，先以原摘要暂存，再经 SQLite backup API 写入 `.work/legacy-import` 内的一次性副本。Go 导入器核对副本摘要、旧版表结构和约束，按旧版序列升级武器、宠物、仓库、勋章、洞察绑定和收据。报告的 `SHA256` 与 `ModuleVersion` 指原快照，`ActivationSHA256` 与 `ActivationVersion` 指升级副本。激活成功后写回原摘要的暂存记录。临时副本随导入退出清理。[升级入口](legacy/upgrade_native.go) · [历史版本定义](legacy/schemas/published.json)

源码构建和发布包内的 `import-save` 均自带旧档升级逻辑。用 `-account` 指定要接收该存档的 PostgreSQL 账号。

```powershell
.\bin\import-save.exe -mode activate -source $oldSave -account $account
```

保存损坏、校验失败或包含未接入的专用状态时，报告列出阻断原因并保留暂存。删除过的角色仍占用原序列曾用 ID。多份旧库出现相同角色 ID 时，激活保留原 ID 的前提是另库中尚未占用该 ID。

## PostgreSQL 备份与恢复

从 `go-server` 目录执行以下命令。使用 Python 3.11 或更高版本、与数据库同主版本的 PostgreSQL 客户端工具。`$dsnFile` 是只供本机读取的 PostgreSQL URI 文件。已配置 `S9A1_POSTGRES_DSN` 或 libpq 服务时可省略 `--dsn-file`。工具可经 `--bin-dir` 指定，本项目已验证的 Windows 17.11 客户端在 `.tools/pg17-client/bin`。[PostgreSQL 官方 Windows 发行入口](https://www.postgresql.org/download/windows/)

```powershell
python -B tools/postgres/save_archive.py backup --dsn-file $dsnFile --archive .work/backups/before-cutover
python -B tools/postgres/save_archive.py restore --dsn-file $dsnFile --archive .work/backups/before-cutover --new-database s9a1_restored
```

备份用 `pg_dump` 的一致性快照保存 `s9a1` 游戏数据和已存在的 `s9a1_import` 旧档暂存，一并生成 SHA-256。恢复先核对摘要，再按源库的编码与排序规则创建新库，通过 `pg_restore` 在单个事务内装入。源库和已有目标库可继续使用。数据库角色与授权、游戏网关的签发文件分别归运维管理。正式切换时先结束旧库写入，再取最后一次快照。[pg_dump](https://www.postgresql.org/docs/17/app-pgdump.html) · [pg_restore](https://www.postgresql.org/docs/17/app-pgrestore.html)

在独立 PostgreSQL 17.11 上已完成恢复演练。13 表21行逐表内容、105列、21个索引、97个约束及身份序列状态一致。原装备 UID、119 字节、角色 ID 1、26 金币、51 Cera、一次 +7 的幂等账本、含最大 uint64 的旧收据和 v35 暂存档均已单独核对。尝试覆盖目标、覆盖备份或恢复已损坏的备份均失败，未写入已有库。演练报告位于 `.work/postgres-backup/reconciliation.json`。

## 验证

旧档离线检查覆盖带 WAL 的 v3 武器、已删除角色的序列上界、v34 的金币和含最大 uint64 的历史收据，以及洞察 v14/v23、水晶 v26/v27 四种历史分支。运行 `.\tools\go.ps1 test -count=1 -run TestLegacy ./internal/storage/legacy`。开发时可用 `.\tools\build-save-upgrade.ps1 -Dotnet $sdk` 构建旧服参考助手，再设置 `$env:S9A1_DOTNET = $sdk` 重跑相同用例。两套实现将分别升级同一份快照，逐表核对行数和内容摘要。手动选用参考实现时显式传入 `-upgrader` 和 `-dotnet`。

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

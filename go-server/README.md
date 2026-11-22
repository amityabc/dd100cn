# S9A1 Go 服务端

本目录是 Go 和 PostgreSQL 服务端的开发工程，目标为同项目的 S9A1 源码与配套百级客户端。玩家通过自有启动器登录独立账号，游戏连接玩家电脑上的原生频道与游戏端口。入站登记已经逐项比对固定旧服，开放委托战斗时是 148 个游戏入口和 3 个频道入口，关闭时是 140 个游戏入口和 3 个频道入口。[72 项功能入口](../docs/功能入口核对.md) · [实施进度](../docs/实施进度.md)

## 构建

在项目根目录开 PowerShell。开发机需有 Go，测试数据库需有 PostgreSQL。脚本优先使用项目的 `.tools/go`、PATH 或 `S9A1_GO` 指定的可执行文件，依赖和构建缓存放在本项目的 `.work` 目录。

```powershell
.\go-server\tools\run.ps1 -BuildOnly
.\go-server\tools\go.ps1 build -o bin/launcher.exe ./cmd/launcher
.\go-server\tools\go.ps1 build -o bin/multiplayer-admin.exe ./cmd/multiplayer-admin
.\go-server\tools\go.ps1 build -o bin/import-save.exe ./cmd/import-save
.\go-server\tools\go.ps1 test ./... -run '^$'
.\go-server\tools\go.ps1 test ./internal/protocol ./internal/questwire -run 'TestRegisteredRequestsMatchOriginalHost|TestDPLUpperTicketPreservesAwakeningAndAwardsScroll'
```

第一条测试命令编译所有 Go 包。第二条运行登记表与完成券离线检查。二者都不需要运行服务或 PostgreSQL。

## 在隔离实例运行

1. 按[旧服基线](../baseline/README.md)构建匹配的 C# 程序集。资源准备脚本使用 `-Dotnet` 选择 .NET 10 SDK，`-LegacyOutput` 可指定旧服 Release 目录。资源清单由匹配的 `Script.pvf`、`sk.dat` 和 profile 生成。准备完成后，Go 游戏服务直接读取生成的资源目录并核对原文件哈希。
2. 启动项目专用 PostgreSQL。脚本的 `-BinDirectory` 接收 PostgreSQL 的 `bin` 目录，默认只监听本机 56439。也可将自备 PostgreSQL 的连接串放在只供本机服务读取的文件里。
3. 启动 Go 服务。开发入口自动分配回环端口，将就绪信息写入 `.work/instance/runtime/go-ready.json`。多人模式另外向 `-GatewayConfig` 给出的地址提供需要账号认证的 TLS 入口。

```powershell
.\go-server\tools\export-characters.ps1 -Dotnet $sdk -LegacyOutput $legacyRelease
.\go-server\tools\export-characters.ps1 -GmOnly -Dotnet $sdk -LegacyOutput $legacyRelease
.\go-server\tools\postgres\Test-Instance.ps1 -BinDirectory $pgBin
.\go-server\tools\run.ps1 -DsnFile go-server/.work/postgres-storage/test-dsn.txt
```

在另一个窗口用 `.\go-server\tools\run.ps1 -Stop` 请求该 Go 实例正常退出。多人签发、私有玩家配置和客户端启动命令见[多人启动器](tools/multiplayer/README.md)。

## 旧存档与协议

导入工具 `-mode inspect` 只读检查 SQLite 一致性备份，`stage` 将表和原始单元格存入 PostgreSQL 的暂存区，`activate` 按 `-account` 指定的账号实际启用可完整投影的旧数据。正式迁移前应在副本上读回角色、物品、钱包与收据。连接串通过进程环境 `S9A1_POSTGRES_DSN` 传给程序。

```powershell
.\go-server\bin\import-save.exe -mode inspect -source $sqliteBackup
.\go-server\bin\import-save.exe -mode stage -source $sqliteBackup -account $player
.\go-server\bin\import-save.exe -mode activate -source $sqliteBackup -account $player
```

固定旧服的路由注册和处理顺序在 `testdata/routes-upstream.json`，可用 `tools/export-routes.ps1` 从旧服的依赖注入容器重新导出。原始报文、旧程序生成的向量和录制脚本在 `testdata/protocol`。`python -B go-server/tools/verify-entry.py` 是联机重放入口，要求运行中的隔离 Go 实例的本地账号角色表为空。[原程序注册表](testdata/routes-upstream.json) · [协议样本](testdata/protocol)

城镇和四房教学分别使用全新的专用 PostgreSQL 数据库，保证原用例中首个角色编号为 1。将各自的数据库连接文件传给服务端和验证脚本。以下命令均在项目根目录运行。日常回归使用仓库中的旧服录包。单独设置 `S9A1_LOG_LEVEL=DEBUG` 可在故障排查时查看连接结束的原生请求号和错误原因。

```powershell
.\go-server\tools\go.ps1 build -o bin/town-state.exe ./tools/town-state
python -B go-server/tools/verify-town.py --dsn-file $townDsnFile
python -B go-server/tools/verify-town.py --measure-only --dsn-file $townDsnFile
# 为下一条命令启动使用另一份空数据库的实例
python -B go-server/tools/verify-town.py --combat --dsn-file $combatDsnFile
```

资源目录当前为内部版本 4。匹配资源导出包括 1,807 个副本布局、37 个城镇副本入口、3 种洞察模式、7 个特殊奖励条目、19 个到期模板、43,408 项商品和 218 个任务券模板。旧资源缺少 45/M0、517/M0 的起点及 233/M0 的地图 17244。用 `export-characters.ps1` 重建资源目录。离线资源准备调用配套 C# 解析器，在线流程由 Go、PostgreSQL 和内嵌压缩模块承担。[资源导出](tools/catalog-export/) · [资源与验收范围](../docs/全量迁移清单.md)

## 发布与旧启动入口

Windows x64 发行包由 `go-server/tools/publish.ps1` 构建。它使用已配对的 PVF 和目录，包含服务、启动器、管理工具、旧档导入器、存档归档工具及离线 PostgreSQL 种子。日常使用可保留旧 `START_SERVER.bat`、`STOP_SERVER.bat` 和 `Tool/Local-Run.ps1`。源码树的 `BuildRun` 使用 Go 编译，发布包沿用已安装程序。带 `StoppedDataBackup` 的启动在生命周期锁内验证已有 PostgreSQL 备份。原客户端完整流程、两台电脑联机和全量存档切换是独立验收事项。[发行说明](tools/publish-readme.md) · [工具接口](../docs/工具交互对照.md)

# S9A1 Go 服务端

本目录是 Go 和 PostgreSQL 服务端的开发工程，兼容目标为同项目的 S9A1 源码与配套百级客户端。玩家通过自有启动器登录独立账号，游戏仍连接玩家电脑上的原生频道与游戏端口。旧服对照和候选版本实测见[迁移进度](../docs/实施进度.md)。

## 构建

在项目根目录开 PowerShell。开发机需有 Go，测试数据库需有 PostgreSQL。脚本优先使用项目的 `.tools/go`、PATH 或 `S9A1_GO` 指定的可执行文件，依赖和构建缓存放在本项目的 `.work` 目录。

```powershell
.\go-server\tools\run.ps1 -BuildOnly
.\go-server\tools\go.ps1 build -o bin/launcher.exe ./cmd/launcher
.\go-server\tools\go.ps1 build -o bin/multiplayer-admin.exe ./cmd/multiplayer-admin
.\go-server\tools\go.ps1 build -o bin/import-save.exe ./cmd/import-save
.\go-server\tools\go.ps1 test ./...
```

## 在隔离实例运行

1. 按[旧服基线](../baseline/README.md)构建匹配的 C# 程序集。资源准备脚本使用 `-Dotnet` 选择 .NET 10 SDK，`-LegacyOutput` 可指定旧服 Release 目录。资源清单由匹配的 `Script.pvf`、`sk.dat` 和 profile 生成。准备完成后，Go 游戏服务直接读取生成的资源目录并核对原文件哈希。
2. 启动项目专用 PostgreSQL。脚本的 `-BinDirectory` 接收 PostgreSQL 的 `bin` 目录，默认只监听本机 56439。也可将自备 PostgreSQL 的连接串放在只供本机服务读取的文件里。
3. 启动 Go 服务。开发入口自动分配回环端口，将就绪信息写入 `.work/instance/runtime/go-ready.json`。多人模式另外向 `-GatewayConfig` 给出的地址提供需要账号认证的 TLS 入口。

```powershell
.\go-server\tools\export-characters.ps1 -Dotnet $sdk -LegacyOutput $legacyRelease
.\go-server\tools\postgres\Test-Instance.ps1 -BinDirectory $pgBin
.\go-server\tools\run.ps1 -DsnFile .work/postgres-storage/test-dsn.txt
```

在另一个窗口用 `.\go-server\tools\run.ps1 -Stop` 请求该 Go 实例正常退出。多人签发、私有玩家配置和客户端启动命令见[多人启动器](tools/multiplayer/README.md)。

## 旧存档与协议

导入工具 `-mode inspect` 只读检查 SQLite 一致性备份，`stage` 将表和原始单元格存入 PostgreSQL 的暂存区，`activate` 按 `-account` 指定的账号实际启用可完整投影的旧数据。正式迁移前应在副本上读回角色、物品、钱包与收据。连接串通过进程环境 `S9A1_POSTGRES_DSN` 传给程序。

```powershell
.\go-server\bin\import-save.exe -mode inspect -source $sqliteBackup
.\go-server\bin\import-save.exe -mode stage -source $sqliteBackup -account $player
.\go-server\bin\import-save.exe -mode activate -source $sqliteBackup -account $player
```

当前正式旧服注册的频道和游戏入口及处理顺序已导出到[原程序注册表](testdata/routes-upstream.json)。`tools/export-routes.ps1` 可从旧服的依赖注入容器重新导出。原始报文、旧程序生成的向量及录制脚本位于[testdata/protocol](testdata/protocol)。轻量复测用 `python -B go-server/tools/verify-entry.py`，要求运行中的隔离 Go 实例的本地账号角色表为空。

城镇和四房教学分别使用全新的专用 PostgreSQL 数据库，保证原用例中首个角色编号为 1。将各自的数据库连接文件传给服务端和验证脚本。以下命令均在项目根目录运行。日常回归使用仓库中的旧服录包。单独设置 `S9A1_LOG_LEVEL=DEBUG` 可在故障排查时查看连接结束的原生请求号和错误原因。

```powershell
.\go-server\tools\go.ps1 build -o bin/town-state.exe ./tools/town-state
python -B go-server/tools/verify-town.py --dsn-file $townDsnFile
python -B go-server/tools/verify-town.py --measure-only --dsn-file $townDsnFile
# 为下一条命令启动使用另一份空数据库的实例
python -B go-server/tools/verify-town.py --combat --dsn-file $combatDsnFile
```

资源目录当前为内部版本 4，包含赛丽亚传送、码头、飞船、活动 NPC 和道路的原始资格条件。已有开发目录可用 `export-characters.ps1` 重建。离线资源准备目前调用配套 C# 解析器，在线 Go 游戏进程、PostgreSQL 和内嵌压缩模块可独立运行。

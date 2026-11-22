# Serve100 Go Windows x64 发布包

套件沿用已安装的匹配客户端和配套启动器。要使用新版空白发行存档，请用 psql 将 Server/runtime/data/release-seed.sql 应用于准备好的 PostgreSQL 数据库。该脚本要求数据库里没有 s9a1 schema，在同一事务写入所有结构和原版发行账号的 1,000,000 点券。然后在 Server/runtime/postgres-dsn.txt 保存这个实例的 PostgreSQL DSN，或给原生服务进程设置 S9A1_POSTGRES_DSN。也可用该连接字符串直接使用已有迁移数据库。

双击 START_LAUNCHER.bat 打开原启动器界面，选择完整客户端目录。仅管理服务时，直接运行 Server/START_SERVER.bat 或 Server/STOP_SERVER.bat。从包根打开 PowerShell，也可使用旧入口 Tool/Local-Run.ps1 -Mode Start -ServerOnly -NonInteractive。

原生服务程序在 Server/artifacts/bin/serve100/release/serve100.exe。它识别 --root、--run-id、--bounded-console、--request-stop、--prepare-client、--check-inputs 及用于预检的 --evidence-root。旧版 GM 工具使用相同命令和本机管道。工具只在 --prepare-client 命令验证当前要求的就绪进程后写入配对配置。

Tool/bin 包含 Go 多人启动器、管理工具和历史存档导入工具。发布清单逐项记录文件哈希，ZIP 旁的 .sha256 文件记录整包哈希。需要原捕获窗口时，将 Diagnostics.FrameMode 设置为 metadata 或 full。正常运行默认不记录应用帧。

## 带停服备份启动

先通过同一实例的停止入口关闭服务。用 Python 3.11 或更高版本与服务器同主版本的 `psql`、`pg_dump`、`pg_restore`、`createdb` 创建新的备份目录。Python 命令可用 `S9A1_PYTHON` 指定，PostgreSQL 工具目录可用 `S9A1_PG_BIN` 指定。备份默认使用 `S9A1_POSTGRES_DSN`，否则读取当前实例的 `Server/runtime/postgres-dsn.txt`。

从发布包根目录执行：

```powershell
. .\Tool\Build-LocalCandidate.ps1 -Root (Get-Location).Path
. .\Tool\Go-Runtime.ps1
Invoke-S9A1StoppedSave (Get-Location).Path 'Work/SaveBackups/before-start' 'backup'
.\Tool\Local-Run.ps1 -Mode Start -ServerOnly -NonInteractive -StoppedDataBackup 'Work/SaveBackups/before-start'
```

`StoppedDataBackup` 使用事先生成的 `save.dump` 与 `manifest.json`。在关服后和实际启动前各验证一次备份哈希、数据库身份、发行程序及当前逻辑数据。任何一项失配都会中止启动，原备份和数据库仍可供检查。源码树用同一备份约定，点载入 `go-server/tools/launcher-runtime.ps1` 并传入 `s9a1` 的绝对路径。发布包的 `BuildRun` 复用已发布的 Go 程序，源码树才调用编译和 Git 更新。

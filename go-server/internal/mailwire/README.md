# 角色邮箱

C120 首次加载城镇时读取角色存档中的邮件、附件和领取转移记录。N97 合并全部收藏邮件和最早的 20 封普通邮件，再按创建时间和邮件编号降序排列。未领取的附件带有原 64 位编号、119 字节物品核心和扩展段。额外普通邮件计入未加载数量。N99 统计所有未删除的未读邮件。

对照样本由原 C# 的投递、收藏、附件实例化以及邮箱投影方法生成，包含 23 封邮件、一份真实结构的堆叠附件、两封附金币的邮件和一封已读收藏。原 C# 主机和 Go 主机均经原 TCP 的 C4 选角、C120 进城、断线重登两次返回相同的 N97 和 N99 正文。两次进城均显示 21 封邮件，2 封待加载，未读总数为 22。此路径读取已保存信箱。领取附件、取金币和删除命令需要各自的持久化事务。[原 C# 样本](testdata/Program.cs)

从 `go-server` 目录运行。第一条快速检查读取随项目保存的原始样本。后续命令在已生成资源目录及已导入专用测试库 `mailbox-v35` 时通过真实 PostgreSQL 和 TLS 网关检查独立 Go Host。脚本会用项目内临时目录和临时端口，并在结束时停止测试进程、移除本次签发的账号文件。

```powershell
.\tools\go.ps1 test -count=1 ./internal/mailwire
.\tools\go.ps1 install ./cmd/serve100 ./cmd/multiplayer-admin
python -B .\internal\mailwire\testdata\live.py mail
```

专用 SQLite 的邮箱字段来自上述 C# 样本，角色由原服务端 C5 创建。导入后使用独立数据库 `s9a1_mailbox_fixture`，连接配置从项目根目录的 `.work/postgres-storage/mailbox-dsn.txt` 读取。运行同一脚本时增加 `--legacy-dotnet D:\dotnet\dotnet.exe` 可在原发布构建和 SQLite 临时副本上复测。[实服脚本](testdata/live.py)

新环境在制作旧服 C5 基线后可运行以下命令生成测试存档与专库。

```powershell
python -B .\internal\mailwire\testdata\live.py mail --prepare
.\tools\go.ps1 run ./tools/postgres/importfixture -name mailbox -source ./.work/mailbox-verify/v35-mail.sqlite3 -account mailbox-v35
```

## C324 查询收件人

玩家在城镇就绪后可查询同一账号的其他角色。成功时 A324 返回角色保存的名字、组号 6、U16 等级、职业、转职和三个保留零字节。英文名字只折叠 ASCII 大小写。查询自身返回 `00 07 00`，查无此人返回 `00 15 00`，未就绪或请求无效返回 `00 02 00`。查询长度含 4 字节小端长度、至多 19 字节名字及 1 字节普通模式 0。末尾填充、NUL、非零模式及四字节 GB18030 输入均被原服拒绝。此接口使用严格 .NET CP936，接受单字节 `80` 和 `FF`。全部 23,940 个双字节码点已逐一核对上游 .NET 结果并测试回写。

同账号名单、选角、城镇加载和退选边界取自完整旧服 TCP 回放。21 组固定样本保存从请求到回复的完整帧，包括查询成功及回复原拼写两种情况。PostgreSQL 使用当前账号、租约持有者和 epoch 约束的一条只读查询，仅读取匹配角色的基本资料。跨账号检查使用 Go 网关下另一个真实签发账号创建的角色。原服在本地单一账号名单中对相同输入返回查无此人。[原服录制](testdata/recipient-legacy.json.gz) · [TCP 脚本](testdata/recipient_live.py)

重采时使用决斗录制工具的 `--save` 双角色 v35 初始快照。在 `go-server` 内运行。常规包回归使用随仓录制。真实网络复测需要两角色初始存档和项目内的专用 PostgreSQL 库。

```powershell
.\tools\go.ps1 test -count=1 ./internal/mailwire
.\tools\go.ps1 run ./tools/postgres/importfixture -name recipient -source ./.work/duel-capture/v35-duel.sqlite3 -account recipient-v35
.\tools\go.ps1 install ./cmd/serve100 ./cmd/multiplayer-admin
python -B .\internal\mailwire\testdata\recipient_live.py
```

将 `--legacy-dotnet` 指向匹配的旧服 .NET 10 SDK 可重新录制原始样本。命令会创建独立临时存档，使用原 C5 建立英文大小写测试角色，结束后收回原服进程和临时文件。原程序集的双字节表以 U16 小端顺序计算 SHA-256，结果为 `30FBE8BD730FB9B3F176D640261C77BE60C102C5D7CCB89F96E510EA000CFE42`。以下命令可直接调用原 C324 解析器与回写函数核对。

```powershell
& $dotnet build .\internal\mailwire\testdata\MailVectors.csproj --disable-build-servers --artifacts-path .\.work\mail-cp936 -c Release
& $dotnet .\.work\mail-cp936\bin\MailVectors\release\MailVectors.dll --recipient-codepage
```

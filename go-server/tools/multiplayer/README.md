# 两台电脑连接同一台服务端

每位玩家在自己的 Windows 电脑上运行 `launcher.exe`。原游戏连接本机 7001 频道端口，以及 10019、10501、10252、10063 四个游戏端口。启动器通过 TLS 1.3 连接运营方网关，每条频道和游戏连接都用发给该玩家的凭据认证。游戏报文保持原格式。

## 服主

以下命令在 `go-server` 目录运行。将示例局域网地址换成玩家可达且与网关证书匹配的主机地址。持有既有数据库连接配置和匹配客户端资源目录的服务端运行 Go Host。

```powershell
.\tools\go.ps1 install ./cmd/launcher ./cmd/multiplayer-admin
$private = Join-Path $PWD '.work/multiplayer'
$operatorDir = Join-Path $private 'operator'
.\bin\multiplayer-admin.exe init -dir $operatorDir -gateway 192.168.1.10:17400
.\bin\multiplayer-admin.exe issue -dir $operatorDir -account player-a -out (Join-Path $private 'player-a.json')
.\bin\multiplayer-admin.exe issue -dir $operatorDir -account player-b -out (Join-Path $private 'player-b.json')
.\tools\run.ps1 -DsnFile ..\.work\postgres-storage\test-dsn.txt -GatewayConfig (Join-Path $operatorDir 'gateway.json')
```

示例使用本项目验证实例的 DSN 文件。正式部署可指定相应数据库的私有 DSN 文件。启动脚本默认读取 `.work/catalog/characters.json`，配置其他已导出的角色目录文件时使用 `-Catalog`。服务端只需向玩家开放网关所用的 TCP 端口。对外域名和进程监听地址分开时，在初始化命令增加 `-listen 0.0.0.0:17400`。直接运行 `serve100.exe` 时，环境变量 `S9A1_GATEWAY_CONFIG` 接受同一份配置文件的路径。

`init` 需要一个新目录。该目录保存网关私钥、公开证书、监听参数及账号凭据摘要。每位玩家通过可信渠道单独领取 `launcher.exe` 和本人的 JSON。玩家文件含账号、私密令牌、对外地址和用于验证网关的证书，请保存在只有本人可读的位置。数据库 DSN 和网关私钥留在服务端。

原服回环入口独占账号 `local`。多人签发工具和 TLS 网关拒绝该账号及大小写变体。已有单机存档要迁给远程玩家时，请将存档导入该玩家独立签发的账号。

## 玩家

在持有匹配版本客户端的电脑上运行，`-client` 指向含 `DNF.exe`、`Start-Client.ps1` 和 `LocalTools` 的解压目录。每台电脑使用自己的配置。

```powershell
.\launcher.exe -profile .\player-a.json -client 'D:\游戏\客户端'
```

认证成功并绑定本机端口后，启动器调用解压目录自带的 `Start-Client.ps1`，原脚本校验文件、准备资源工具、检查本机频道端口并调用配套启动程序。请按原流程处理管理员权限提示，游戏期间保持启动器运行。资源工具需要 .NET 10 x64。使用自定义安装目录时，追加 `-dotnet-root 'D:\dotnet'`。已有相同本机端口的服务进程会使启动器返回占用错误。

本次检查的解压包包含 SHA-256 为 `82726B29688FE82D2476EF68854F8984040D7EF34B87BC53849FBA04BEFC9D31` 的配套 `LocalTools/DNF100Launcher.exe`。包内 `Start-Client.ps1` 以本机频道连接判断服务就绪。另一份 `s9a1/ClientTools/Start-Client.ps1` 要求旧服进程、哈希和就绪记录，部署时应保持使用解压包内部的配套脚本。

## 复核

```powershell
.\tools\multiplayer\check-client.ps1 -ClientRoot ..\客户端
.\tools\go.ps1 test ./internal/multiplayer -run TestTwoAccountsCarryUnchangedLegacyFrames -count=1
```

客户端检查在项目内建临时硬链接目录，调用解压包原脚本的 `-CheckOnly` 和 `-PrepareOnly`。该检查要求客户端与项目位于同一支持硬链接的卷。网络测试在同一 IP 上模拟两台电脑，对照原 C# 服务端采集的真实回包，并检查无效凭据在建立游戏连接前被拒绝。

为正在运行的测试网关签发两个全新账号，可复测 PostgreSQL 持久化。此命令会在这两个账号内各创建一个角色，并保留数据库记录。

```powershell
$env:S9A1_TEST_PLAYER_A = (Resolve-Path '.\.work\multiplayer\test-a.json').Path
$env:S9A1_TEST_PLAYER_B = (Resolve-Path '.\.work\multiplayer\test-b.json').Path
.\tools\go.ps1 test ./internal/multiplayer -run TestLivePostgresTwoAccounts -v -count=1
```

2026-11-23 的实测使用运行中的 Go Host、TLS 网关和项目 PostgreSQL。两个账号同时在线，分别通过 C5 建角及 C4 选角，收到 N1378 等级。第二个账号尝试使用第一个账号的角色名，回包正文是 `00 12`。两个连接分别断开并重连后，C8 各自只显示本账号的角色，再次 C4 返回的角色编号与建角时一致。另一账号在每次重连过程中均收到 C1715 回显。另在两个账号分别保存不同的设置与快捷键，重连后各收到自己原有的 N173 和 N440。保持原连接再次登录同一账号时，旧连接关闭，另一玩家继续收到心跳。最新一次用例耗时 0.19 秒。单机双代理使用独立的临时监听端口以模拟两台电脑。

另一次运行直接使用编译出的 `launcher.exe -bridge-only` 绑定五个原始端口。7001 收到原 C11 对应的频道回应 12。四个游戏端口均收到 N1 和 ACK1，登录回包公布本机地址、对应固定端口及原频道类型。原资源工具的两个准备入口均成功。图形客户端的实际登录与游戏画面仍需实机确认。

## 决斗频道

两个决斗入口的对照使用同一份原服 v35 双角色存档和真实 TLS 账号。251、252 分别回放预进入、C4 选角、C120/C143 同步、C35/C623 移动、C36 道路及其重放、C7 返回和低等级拒绝。共 50 个请求，127 个操作应答帧逐字节一致。六次自动登录核对每个频道自己的固定端口、挑战字段和时间。每次退出后，PostgreSQL 的角色等级、版本、城镇位置和完整玩法 JSON 均与原 SQLite 记录一致。原样本将第一个角色设置为 50 级，并由原 C5 创建第二个 1 级角色。[双频道原始记录](../../testdata/protocol/legacy-duel-lobby.json.gz)

从项目现有的新鲜决斗存档生成独立测试库，给每轮首入验证选择一个新库名。下面的 `libpq.dll` 使用项目 PostgreSQL 进程附带的客户端库，供脚本在六次断开后读回角色。测试进程和临时凭据仅在脚本运行期间存在。

```powershell
.\tools\go.ps1 install ./cmd/serve100 ./cmd/multiplayer-admin
.\tools\go.ps1 run ./tools/postgres/importfixture -name duel_replay02 -source ./.work/duel-capture/v35-duel.sqlite3 -account duel-v35
$pgBin = Split-Path (Get-Process postgres | Select-Object -First 1 -ExpandProperty Path)
python -B .\tools\multiplayer\verify-duel.py --dsn-file ..\.work\postgres-storage\duel_replay02-dsn.txt --libpq (Join-Path $pgBin 'libpq.dll')
```

## 在线契约到期

普通频道 C4 携带仍生效的霸王契约 22、达人契约 27 和晶之契约 97 的剩余秒数。随后的 N66 还包括成长契约 84。决斗频道只在 C4 显示 97。原服和 Go 实例均在玩家下一次发包时检查过期情况，同一连接只发一次归零通知。原存档保留已过期截止时间时，重连 C4 返回空契约列表，并以 N66 再次清除对应状态。晶之契约自然到期保留角色的晶块选择。交易造成的续期、购买、撤销和晶块选择 C527 还需要各自的事务及协议接入。

测试在已导入的专库内临时设置 4 秒期限，使用同一连接跨越到期时刻，再断开重连，结束时恢复测试账号原有截止时间。以下库名和账号专供此用例。也可增加 `--legacy-dotnet D:\dotnet\dotnet.exe` 在临时 SQLite 副本上运行原发布构建。[计时验收脚本](verify-premium.py)

```powershell
.\tools\go.ps1 run ./tools/postgres/importfixture -name premium_expiry -source ./.work/storage-verify/v35-town-equipped.sqlite3 -account premium-expiry-v35
python -B .\tools\multiplayer\verify-premium.py --dsn-file ..\.work\postgres-storage\premium_expiry-dsn.txt --libpq (Join-Path $pgBin 'libpq.dll')
```

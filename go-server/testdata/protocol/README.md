# S9A1 服务端协议对照

从仓库根目录运行 `./go-server/tools/go.ps1 test ./internal/protocol ./internal/opening ./internal/duellobby`。平时的回归读取这些固定样本，只有重新采集才会启动原服。所有 TCP 请求均由本地脚本发出，原客户端界面和客户端操作时序还需实机验收。

| 文件 | 来源 | 校验内容 |
| --- | --- | --- |
| `public-frames.json` | 上游 `Server/tests/Serve100.Tests/Fixtures/PublicSamples.json` 中的 55 个合成帧 | 按方向解析并重建完整帧，包括 11/13/16 字节头、CRC、marker16、序号和原始负载。上游文件 SHA-256 为 `859688EE721746F6ED0DD4DE7F535225B07E03ADD653F5037C7A395239B44E4D`。 |
| `legacy-empty.json` | 原 C# Release Host、匹配的 PVF、独立空账号 | 频道 C11/C9/C17、日期密钥、脚本密文、目录密文及四个实际监听端口。游戏连接包括主动 N1/ACK1、空选角列表、名字成功与拒绝、心跳和无角色选角后断连。N1 的时钟与随机材料从样本取回，再重建全帧。 |
| `entry-bodies.json` | `Export-EntryVectors.ps1` 直接调用原 C# 协议程序集的公开编码函数，输入是固定的角色和武器 | 非空列表、创建成功、两种选角分支、71/140 字节角色描述、Mode1 简版/完整版、Mode3、位置、城镇、疲劳、账户设置和快捷键逐字节比对。 |
| `legacy-town.json.gz` | 原 C# Release Host、匹配的 PVF、新建职业 0 角色 | 创建、选角、待教学的第一次 C120、C37、C191、C45 拒绝，以及重连后 C143 跳过教学、两次 C120 入城、C35 移动。每一步保存完整请求、原始回复和同一次运行中角色表的状态。 |
| `legacy-tutorial-combat.json.gz` | 单独启动的同版本旧服，使用同一匹配 PVF 导出的职业 0 四间教学布局 | 保存正常开场、加载、按房间发送的 C39/C45、角色状态及最终旧库收据。样本中的每次实际接受、重试和拒绝均可由原帧与角色修订号核对。 |
| `legacy-travel.json.gz` | 同版本旧服完成上一组城镇动作后，重新选入已过教学的角色 | C12/C13 普通单成员队伍和拒绝公告、城镇道路 C36、离房 C1418、移动后重放及 C7 退选。保存每步实际回包、坐标和版本。 |
| `legacy-duel-lobby.json.gz` | 同版本旧服和 PVF，隔离的 v35 双角色存档。第一个角色来自原服 C5，单独在副本中设置 50 级、教学已完成。第二个 1 级角色由原服 C5 正常创建 | 包括频道 251/252 原生选角、低级拒绝断连、C195、C120/C143 两种入场、C35/C623、C36 成功与拒绝、重复换区及 C7。每个操作有原始 TCP 字节与持久角色快照。 |
| `compression-hashes.json` | `CompressionVectors.cs` 使用与原 Host 相同的 .NET 10.0.15 和 `ZLibStream.Write`，生成确定性测试数据 | 比较空写入、8 KiB、跨 64 KiB 和 1 MiB 的伪随机输入与混合零字节输入，逐项对照压缩长度及完整 SHA-256。 |

原 Host 的 SHA-256 为 `FF020BE723CC83E3D5FA135B6649F0E21F2D0512873B7A1ACC20DBEC28A30B46`。具体运行 ID 和程序集摘要随样本保存。采集使用原规则、原字节码、真实资源和隔离存档。独立的游戏资源导出位于项目的本地工作目录。

## 已接入入口的字段速查

正式配置中的完整入站清单含三个频道入口和 148 个游戏入口，逐条保留了旧 Host 的处理程序顺序。下表给出已实现路径上容易因语言或多人改造而变化的字段。C 代表请求，N 代表通知，ACK 代表对应的应答。表中长度均为字节数，整数按原生小端编码。[正式路由](../routes-upstream.json) · [帧读写](../../internal/protocol/frame.go)

| 入口 | 客户端发送 | 客户端或数据库应当看到 |
| --- | --- | --- |
| 频道 C11 / C9 / C17 | 依次为 32、0、24 字节。目录也可以在取得脚本前请求。 | 频道包头 11 字节。客户端看到原目录、脚本和玩家电脑本地的频道端口。远端账号凭据在启动器与网关之间传递。 |
| 游戏连接 / C8 / C679 | 游戏请求头 13 字节，常规应答头 16 字节。C8 固定为 `FF FF 02`，空名册探测为 `20 00`。 | 建连时主动 N1、ACK1，随后按存档恢复选项。C8 返回 N2。空名册后的 C679 有自己的应答和请求次序。 |
| C5 建角 / C4 选角 | C5 为职业、32 位名字字节数、原代码页名字和建角选项。C4 为 16 位名册下标。 | C5 成功回传 16 位角色 ID 与名字，随后 N2。C4 的下标和持久角色 ID 分开保存。普通频道按配置发 N1378、N1734 和有效契约通知。 |
| C35 / C623 / C36 / C1418 | C35 为 7 字节，含坐标、九宫格方向和速度。C623 为 13 字节，含表现状态、状态参数与保留位。普通 C36 为 21 字节，有原活动格式使用 24 字节。C1418 为空。 | 原城镇坐标、角色朝向、回归点及角色版本按场景更新。普通成功移动没有独立 ACK。道路重试保留原有包序。 |
| C12 / C13 | C12 支持原自定义标题与预设标题两种形态。C13 含 4 字节退出参数。 | 普通单成员队伍的 ACK、N9、拒绝公告与重试已录制。双人进出视野及成员同步仍缺同版本实录。 |
| C19 普通物品移动 | 28 字节，保留源/目标容器、槽位、模板、数量、动作角色和末尾两个状态字节。 | 11 字节的成功正文返回实际源槽、数量和目标槽。持久层保留原实例 UID 和 119 字节物品核心。[资产协议](../../internal/inventorymove/wire.go) |
| C31 接任务 / C32 放弃 | 保留客户端发送的任务号与原请求长度，按角色状态和资源中的接取、放弃条件判断。 | 相同接取重试的应答次数与旧服一致。正常接受的任务更新 N561、N21、N356。首次发放任务物品时，按原顺序同时更新新增背包行和穿戴。[实测样本](../../internal/questwire/testdata/legacy-commands.json.gz) |
| C120 / C143 / C195 | 普通 C120 无正文，C143 为 6 字节，C195 无正文。 | 教学、普通城镇、自由及排位决斗大厅各自依原阶段发包。决斗技能页和城镇持久位置分开，首次进城时的共享材料归并先提交再投影。 |
| C197 / C198 | 32 位长度前缀，游戏选项正好 372 字节，快捷键快照为 1～1399 字节。 | 按账号原样保存。重连时 N173 为长度、原选项和 8 字节零尾，N440 为零字节、长度和原快捷键。 |
| C324 邮件收件人 | 32 位名字字节数，最多 19 字节严格 CP936，末尾 1 字节普通模式。 | 成功返回原名、组号、等级、职业和转职。保留未就绪、自发、缺失收件人的不同错误码。[同账号和跨账号原服对照](../../internal/mailwire/README.md) |
| C1715 / C63 / C3 | 心跳为 13 字节客户端数据及 4 字节 tick。C63 为空，C3 含 1 字节原因。 | ACK1715 为 `01` 加原 tick。账户余额由 ACK63、N53 返回，N53 保留原三栏布局。C3 退出应答正文为 `01 00 00 00 00`。 |

## 包序和持久状态

- 开始选角收到 ACK4、N1378、N1734。原生开场阶段 C120 同步后还有 N27、N5、N2 Mode0、N2 Mode1、N28、N29、N36、完整 N2 Mode1、N120。N29 的四字节随机种子属于每次副本实例。
- 首间 C37 支持 8 或 16 字节。旧服第一次发送 ACK37、N37、N30、N120、N36、N3，再由特殊奖励模块发送 N1734。相同房间重复加载只发送 ACK37。C191 的 `01 00`、`00 03` 各产生一次 N170，`00 01` 静默。未清房通往下一间的 C45 收到 `00 04 00`。
- 首房就绪后，空正文 C42 撤离教学并按 N23、N24 顺序返回城镇坐标。角色在同一事务内恢复满 HP/MP 并清除城镇显示状态，已扣疲劳和未完成教学标记保留。收到 C35 前重复 C42 只重发 N23、N24。房间未就绪且没有待确认的回城时，原服按协议错误断开连接。`TestOpeningDungeonReturn` 使用独立 PostgreSQL 测试库验证 Go 路由、回包顺序和重复请求的持久化结果。
- 回城确认 C35 的邮箱恢复投影只发送 N97，接着发送 N19 技能页，城镇恢复以 N120 收尾。首次进城的邮箱投影在 N97 后发送 N99 未读数。`TestOpeningDungeonReturn` 的 `C35 recovery mailbox` 子测试用配套角色目录和 PVF 验证这段回包。
- 首次直接完成指引的 C143 六字节为 `00 1F 00 00 00 01`，返回 ACK143、完整 N2 Mode1、N23、N24。其他六字节保持原义。创建角色时已有的任务、设置和账号服务会影响后续 C120 的通知。
- `capture_town.py` 在每项操作后发送 C1715，以带唯一 tick 的 ACK1715 标出此次网络读取的边界。个别异步通知会在前一条心跳之后到达。查看原帧中的序号即可辨认。marker16 使用固定头和序号 0。
- 在四间正常教学，职业 0 的 16 个阻塞怪物每只给 30 点经验。原库记录 16 份击杀收据和 480 点经验。重复 C39 不增加经验。入场扣除 4 点疲劳，最终角色修订号 18，教学标记在最后一间结束时置为已完成。
- 普通决斗和排位频道均允许本次 50 级角色直接发 C4，答 ACK4、N1378。两个 1 级测试都在 C4 后断连，回复数为零。首个 C143 的包序为 ACK143、N376、N2(0)、N2(1)、N124、N9、N22、N23、N53、N24、N41、N561、N21、N356、N376、N19。同一次 C120 入场在 ACK120 之前还有 N108。C195 只在就绪时返回 `01 00 00 00 00 00`，就绪前/退选后均返回 `00 15 00`。
- 首次入决斗大厅时，旧服把常规技能、SP/TP 和已有按键锁复制到 `DuelSkills`。样本角色首次修订号 0 → 1。后续两频道的同步和移动均保持修订号 1，持久城镇坐标为 38/0 (1677,222)。两个大厅的会话出生坐标为 10/0 (474,234)。排位频道的 C36 可往返 4、1、0 区，成功及紧接的相同重试都发 N23、N24。自由频道、入场过早、经过其他传送后的过期重放，均返回 ACK36 拒绝。

## 重采

在同版本的 `baseline/.work` 已完成 Release 构建与资源配置后，可运行以下命令。将 `$python` 和 `$dotnet` 设为本机对应解释器的绝对路径。空账号和决斗频道采样需要 Python 的 `cryptography` 解开原频道目录。决斗采样使用 `baseline/.work/fixtures/v35-character.sqlite3` 的独立副本。`--save` 可选，可保留首次进入大厅前的完整 v35 在线备份，供 PostgreSQL 导入后回放。请将新采样写入项目内单独文件以便核对。

```powershell
& $python -B go-server/testdata/protocol/capture_legacy.py $readyFile $newEmptySample
& $python -B go-server/testdata/protocol/capture_town.py --baseline baseline/.work --dotnet $dotnet --output $newTownSample
& $python -B go-server/testdata/protocol/capture_town.py --baseline baseline/.work --dotnet $dotnet --combat-catalog go-server/.work/catalog/characters.json --output $newCombatSample
& $python -B go-server/testdata/protocol/capture_town.py --baseline baseline/.work --dotnet $dotnet --travel --output $newTravelSample
& $python -B go-server/testdata/protocol/capture_duel.py --baseline baseline/.work --dotnet $dotnet --output $newDuelSample --save go-server/.work/duel-capture/v35-duel.sqlite3
```

角色名字和宠物文字使用原 .NET 代码页 54936。`Generate-NameTable.ps1` 枚举该提供程序的双字节表与四字节范围，并验证回读。Go 的嵌入表为 48,704 字节，SHA-256 为 `04F3BA229FB754A517E4224850A1B9A8E986D00C1407A11B63C1D302596825A6`。

## 原格式压缩

原 .NET 10.0.15 的 `System.IO.Compression.Native.dll` 使用 zlib-ng 2.2.5。不同实现可把同一份任务位图编码为不同字节。Go 服内嵌使用相同算法、固定 level 6 的 WebAssembly，复核包含实际旧服 N356 的 30,004 字节输入和完整 55 字节压缩流，以及原频道的脚本、目录密文。原 .NET 空写入后关闭会输出零字节。构建和运行游戏服只使用 Go。Wazero 在首次使用时编译模块，按需创建最多八个实例。单次原文最多 1 MiB，每个实例的线性内存最多 8 MiB。模块只导入编译运行库所用的随机字节函数。并发测试覆盖空输入、普通内容和上限值。

重新生成时，在项目目录内备好 Zig 0.13.0 和原版 zlib-ng 2.2.5 的 `tar.gz` 压缩包。生成器检查版本及源码包 SHA-256 `5B3B022489F3CED82384F06DB1E13BA148CBCE38C7941E424D6CB414416ACD18`，使用 `wasm32-wasi` 通用实现、`-Oz` 与 64 KiB 栈，以 reactor 形式编译。包装入口使用窗口 15、内存级别 8、不插入中途 flush。两次生成均得到 40,375 字节，SHA-256 为 `45D75F9EB2C0279C04B3CAC92725E17A8C807D58C2FE589EC3F96690BD645BA3`。全部编译开关在 `build_zlib.py` 中。[zlib-ng 2.2.5 源码包](https://codeload.github.com/zlib-ng/zlib-ng/tar.gz/refs/tags/2.2.5) · [Zig 0.13.0](https://ziglang.org/download/0.13.0/release-notes.html) · [zlib 许可](LICENSE-zlib-ng.md) · [WASI C 运行库许可](LICENSE-wasi-libc.txt)

```powershell
& $python -B go-server/testdata/protocol/build_zlib.py --zig $zig --archive $zlibNgArchive
Get-FileHash -Algorithm SHA256 go-server/internal/protocol/zlibng.wasm
```

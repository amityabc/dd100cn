# dd100cn

匹配 S9A1 百级客户端的 Go / PostgreSQL 服务端。工程包含游戏与频道服务、玩家连接网关、旧 SQLite 存档导入工具，以及和原 C# 服务端比对用的协议样本与性能基线。

## 构建和测试

在 Windows 上安装 Go 1.26 或更高版本，在仓库根目录用 PowerShell 执行以下命令。服务程序输出到 `go-server/bin/serve100.exe`。

```powershell
.\go-server\tools\run.ps1 -BuildOnly
.\go-server\tools\go.ps1 test ./...
.\go-server\tools\go.ps1 vet ./...
```

## 本地运行

本地联调使用 PostgreSQL 17 和与客户端版本一致的资源。需要原 C# 工程及配套工具时，在仓库根目录把上游取到约定的 `s9a1/`。协议基线固定在以下提交。

```powershell
git init s9a1
git -C s9a1 fetch --depth=1 https://codeberg.org/hongye/S9A1.git c7e7843332f91c31a0833135f3dd847dfa90cc21
git -C s9a1 checkout --detach FETCH_HEAD
```

将自备客户端放入本机的 `客户端/`。资源准备、测试数据库、启动命令和多人网关配置见[服务端使用说明](go-server/README.md)。原客户端、游戏资源、私人存档和实例连接配置都保留在本机。文档中指向 `.work/` 的实测日志随各自的测试实例生成。

## 兼容状态

已在原客户端实测建角、选角、进入教学和返回城镇。冒险团面板缺少匹配上游的处理逻辑，QuickTest 的游戏内加载仍待验证。跨机多人同场另行验收。[实机记录及已知缺口](docs/实施进度.md) · [原服功能对照](docs/功能入口核对.md) · [性能基线](baseline/README.md)

PVF 读取、兼容压缩模块及其运行库的第三方许可随对应组件保存。[PVF 组件许可](go-server/internal/pvf/LICENSE) · [zlib-ng 许可](go-server/testdata/protocol/LICENSE-zlib-ng.md) · [wasi-libc 许可](go-server/testdata/protocol/LICENSE-wasi-libc.txt)

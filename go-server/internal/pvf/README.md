# S9A1 原始 PVF 读取

此包将旧端的 [NativePvf100](../../../s9a1/Server/src/Serve100.Infrastructure/Resources/NativePvf100) 和 [PvfResourceProvider](../../../s9a1/Server/src/Serve100.Infrastructure/Resources/PvfResourceProvider.cs) 只读格式迁入 Go。运行依赖 Go 标准库、仓库现有的 `golang.org/x/text` 解码器和 Windows 文件共享接口。旧版格式的 MIT 版权声明见 [LICENSE](LICENSE)。

`Open(path, profile)` 支持旧端识别的 `protected_nkpi`、`client100_nkpi` 和 `client100_sk_nkpi`。最后一种需要显式提供现有的外部 Client100 JSON 配置，以及与 PVF 同目录的 `sk.dat`。包内只保留目录、字符串池、少量区域头和按需解压的正文缓存。调用方可以并发查询同一份文件，并用 `Close` 释放文件句柄。

- `Read`、`ReadText` 分别读取原始字节和解码后的正文。常规原始字节上限为 1 MiB，点券商店和命运抉择脚本通过各自的命名方法沿用旧端独立预算。
- `ReadMapText` 保留地图显示名的原始字符串引用，其他字符串仍沿用本地化解析。`LocalizedText` 和 `ResolveString` 可分别解析本地化引用和二进制正文中的字符串池偏移。
- `Paths` 按原目录顺序枚举，`PhysicalIndex` 提供正文的压缩块和块内偏移。批量读取可按块排序。`ReadIndex` 按原格式读取资源编号和路径。命运抉择传送对象和独立掉落有专用索引。
- `ExperienceThresholds` 保留原始类型 0 数值与类型 9 高低字配对，并返回原表全部记录。经验等级上限由消费此资源的进度模块应用。

执行 `go test ./internal/pvf` 可用项目现有的 `客户端/Script.pvf` 校验兼容性。测试比较 C# 读取器独立得出的两类索引规模，以及任务道具、命运抉择、点券商店和 150 条经验阈值的 SHA-256。文件缺失时跳过真实资源测试。

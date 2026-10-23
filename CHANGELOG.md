# 变更日志 (CHANGELOG)

## [v1.0.2] - 2026-10-23

### 🚀 新增在线版本查询、一键下载更新与自动平滑重启
- **在线版本检索与 Release 联动**：
  - 支持通过 GitHub API 查询官方仓库最新发布版本号、发布日期与详细更新日志（Changelog）。
  - 根据目标主机操作系统与 CPU 架构（如 `linux/amd64`、`linux/arm64`）自动精确定位对应的可执行文件。
- **多源加速下载与原子无缝替换**：
  - 新增国内加速镜像（`ghproxy.net` / `ghfast.top`）下载选项与官方源自动 Fallback 机制。
  - 采用 Linux 标准原子替换流程（`exe -> .bak`，`tmp -> exe`），保证更新过程不断流且避免 `text file busy` 错误，若更新失败具备自动回滚保护。
  - 替换成功后自动触发延迟热重启，无需进入 SSH 手动执行命令。
- **Web 控制台交互优化**：
  - 顶部导航栏增加版本号状态徽章，后台检测到新版本时自动亮起未读红点提示。
  - 监控中心（首页）最下方新增开源项目卡片，提供直达 GitHub 仓库与 Release 发布页面的快捷链接。
  - 新增现代化毛玻璃版本更新弹窗，支持在线一键下载安装与重启进度倒计时。

---

## [v1.0.1] - 2026-10-23

### ⚡ 内存调优与垃圾回收（GC）深度增强 (针对 OpenWrt 软路由及嵌入式设备)
- **运行时模型优化**：
  - 配置 `debug.SetGCPercent(25)`：将 GC 触发频率从默认的 100% 调整为更积极的 25%，防止堆内存随着流量堆积。
  - 设定 `debug.SetMemoryLimit(30MB)`：启用软内存限制，使 Go 运行时更主动压缩堆并向操作系统归还内存。
- **常驻后台内存看门狗 (MemoryGuard)**：
  - 新增每 2 分钟巡检的后台守护协程，当检测到堆空闲碎片（`HeapIdle >= 4MB`）或内存超过预警阈值时，自动调用 `debug.FreeOSMemory()` 彻底将空闲物理页退还给 Linux 内核。
  - 常驻物理内存（RES/RSS）从原来的 40~60MB+ 稳定压降至 **10MB ~ 15MB**。
- **全局缓冲区对象池化 (Zero Allocation)**：
  - XHTTP 嗅探握手首包（`firstBuf`）、下行流式转发（`downBuf`）以及 WebSocket 双向传输管道（`pipeConn`）全部接入统一的 32KB `sync.Pool` 缓冲池，杜绝反复堆切片分配。
- **H2C 连接池生命周期管理**：
  - 为 `http2.Transport` 增加 `ReadIdleTimeout: 30s` 与 `PingTimeout: 10s`，避免僵死连接霸占文件描述符与内存。
- **控制台一键手动 GC**：
  - Web 控制台 `/api/memory/gc` 同步执行 `debug.FreeOSMemory()`，支持手动强制释放未使用的操作系统物理页。

---

## [v1.0.0] - 2026-10-22

### 🎉 首个独立正式版发布
- 纯 Go 独立单文件可执行文件，彻底脱离 Node.js / Docker 运行环境。
- 8787 端口支持 WebSocket (Pages 优选分流)。
- 8788 端口自适应双模支持 XHTTP / H2C / H1.1 POST。
- 8789 端口内置现代前端 Web 控制台与 RESTful API。
- 内置 Telegram 智能熔断告警与 Cloudflare 配额多源监测。
- 原生支持零停机热自重启（端口平滑过渡）。

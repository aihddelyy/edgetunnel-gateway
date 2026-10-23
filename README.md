# EdgeTunnel LB (Go 高性能单文件独立版)

基于 Go 语言全新重写的 Cloudflare 边缘分流智能聚合网关。针对多子账户 Cloudflare Workers / Pages 免费配额深度优化，提供**协议嗅探双模自适应、128KB 握手首包无感故障转移、多账户 API 实时配额避让及 Web 可视化管理**。

---

## 🌟 核心特性与优势 (对比 Node.js 原版)

| 指标 / 特性 | Node.js 原版 | Go 重构高性能独立版 | 提升说明 |
| :--- | :--- | :--- | :--- |
| **内存常驻 (RSS)** | ~100MB - 250MB | **~15MB - 25MB** | **降低 85%+**，极大降低路由器与 NAS 负担 |
| **多核并发** | 依赖 Cluster 多进程 + IPC | **原生 Goroutine 调度** | 真正的单进程多核高并发，无 IPC 通信开销 |
| **交付形式** | 需 Node.js 运行时或大镜像 | **单静态二进制 (Single Binary)** | `//go:embed` 内嵌前端，零外部文件与环境依赖 |
| **部署模式** | 繁琐依赖安装或 Docker | **单个文件直接运行 (nohup / systemd)** | 纯粹单文件，随拷随跑，启动仅需 5ms |
| **软路由适配** | OpenWrt 部署困难 | **原生支持 MIPS / ARM / x86_64 交叉编译** | 完美兼容 OpenWrt、Alpine、Debian、群晖 NAS |
| **平滑重启** | 容器外部拉起 | **内置延迟自脱离自拉起 (Self-Restart)** | Web 控制台一键重启，自动解绑端口后无缝接管 |

---

## 🛠️ 架构与端口映射

- **`8787` 端口**：Pages 专属，处理 HTTP/1.1 WebSocket Upgrade 全双工数据流。
- **`8788` 端口**：Worker 专属，TCP 首包 24 字节嗅探双模自适应（自动识别 RFC 7540 H2C 与 HTTP/1.1 POST）。
  - 内置 **128KB 握手首包内存暂存区**：遇到 CF `1101` / `502` / `429` / `521` 时，网关层自动换节点重试并重放数据，**客户端完全无感不断流**。
- **`8789` 端口**：Web UI 管理控制台与 REST API。

---

## 📂 项目结构

```text
go_version/
├── cmd/
│   └── server/
│       └── main.go           # 服务主入口与平滑退出管理
├── pkg/
│   ├── config/               # 配置加载、备份快照、回滚与热更新
│   ├── model/                # 核心数据模型与结构体
│   ├── logger/               # 内存环形日志缓冲区与 SSE 实时推送
│   ├── notify/               # Telegram Bot Markdown 告警 (支持代理)
│   ├── prefer/               # 优选 IP/Host 解析清洗、延迟测速与活跃统计
│   ├── health/               # 熔断器 (连续 5 次 60 秒隔离) 与配额避让轮询调度
│   ├── quota/                # Cloudflare 官方 GraphQL / UsagePanel 额度自动巡检
│   ├── proxy/
│   │   ├── ws.go             # 8787 端口 WebSocket 反向代理
│   │   ├── xhttp.go          # 8788 端口 24 字节嗅探 + 128KB Failover 重试
│   │   └── pool.go           # 高性能缓冲区内存池 (sync.Pool)
│   └── web/
│       ├── web.go            # 8789 端口 REST API 与 SSE 接口 (内置免依赖自重启)
│       └── public/           # 内嵌的前端 Web 控制台静态资源 (HTML/CSS/JS)
├── build.ps1                 # Linux AMD64 & ARM64 一键极速交叉编译脚本
├── config.example.json       # 配置示例模板
└── go.mod
```

---

## 🚀 编译与运行

### 1. 极速交叉编译（Windows PowerShell）

项目内置了 [build.ps1](build.ps1) 编译脚本，一键构建 Linux AMD64 (x86_64) 与 ARM64 (aarch64) 单二进制：

```powershell
.\build.ps1
```

编译产物：
- `edt-gateway-linux-amd64`：适用于主流 x86 软路由、PVE、ESXi、群晖 NAS、常规 VPS。
- `edt-gateway-linux-arm64`：适用于 NanoPi R4S/R5S、斐讯 N1、树莓派等 ARM 架构。

---

### 2. 部署运行（OpenWrt 软路由 / VPS）

#### 首次运行配置
```bash
# 进入部署目录（以 /root/edgetunnel-lb 为例）
cd /root/edgetunnel-lb

# 创建配置文件（可根据 config.example.json 修改）
cp config.example.json config.json

# 赋予可执行权限
chmod +x edt-gateway-linux-amd64

# 后台启动
nohup ./edt-gateway-linux-amd64 -config config.json > app.log 2>&1 &
```

#### 开机自启动（OpenWrt 系统）
在 `/etc/rc.local` 中添加（位于 `exit 0` 之前）：
```bash
cd /root/edgetunnel-lb && nohup ./edt-gateway-linux-amd64 -config config.json > app.log 2>&1 &
```

---

## 🌐 访问 Web 控制台

启动后在浏览器打开：
`http://<设备IP>:8789`

首次打开按提示设置管理员初始密码，进入控制台可享受：
- 节点实时存活检测、丢包率与成功率（精确至两位小数）；
- Cloudflare 官方 API 直连 / UsagePanel 镜像额度实时巡检；
- D1 SQL、R2 存储、KV 空间指标详情展示；
- Web 控制台右上角【重启网关】一键平滑自重启（自动解绑端口后无缝拉起）。

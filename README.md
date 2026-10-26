# edt-gateway (Go 高性能单文件独立网关)

基于 Go 语言全新重构的 Cloudflare 边缘分流智能聚合网关 `edt-gateway`。针对多子账户 Cloudflare Workers / Pages 免费配额深度优化，提供**协议嗅探双模自适应、128KB 握手首包无感故障转移、多账户 API 实时配额避让及 Web 可视化管理**。

---

## 🌟 核心特性与优势 (对比 Node.js 原版)

| 指标 / 特性 | Node.js 原版 | edt-gateway (Go 重构高性能版) | 提升说明 |
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

#### 方式一：OpenWrt 系统级全自动一键安装（强烈推荐）
在 OpenWrt 终端（SSH）执行以下命令，自动完成架构识别、安装、专属独立用户创建、系统服务与开机自启、固件升级防丢失配置：

```bash
# GitHub 原生链接
curl -sSL https://raw.githubusercontent.com/aihddelyy/edgetunnel-gateway/go_version/install.sh | sh

# 国内加速镜像链接（网络受限时使用）
curl -sSL https://ghfast.top/https://raw.githubusercontent.com/aihddelyy/edgetunnel-gateway/go_version/install.sh | sh
```

#### OpenWrt 一键卸载与清理
若需要卸载网关服务：
```bash
# 默认保留配置文件与历史备份（推荐）
curl -sSL https://raw.githubusercontent.com/aihddelyy/edgetunnel-gateway/go_version/uninstall.sh | sh

# 国内加速镜像
curl -sSL https://ghfast.top/https://raw.githubusercontent.com/aihddelyy/edgetunnel-gateway/go_version/uninstall.sh | sh

# 彻底清除所有数据（包括配置文件 /etc/edt-gateway）
curl -sSL https://raw.githubusercontent.com/aihddelyy/edgetunnel-gateway/go_version/uninstall.sh | sh -s -- --purge
```

#### 方式二：手动轻量运行（适合常规 Linux VPS）
```bash
# 进入部署目录（以 /root/edgetunnel-lb 为例）
cd /root/edgetunnel-lb

# 创建配置文件（可根据 config.example.json 修改）
cp config.example.json config.json

# 赋予可执行权限并后台启动
chmod +x edt-gateway-linux-amd64
nohup ./edt-gateway-linux-amd64 -config config.json > app.log 2>&1 &
```

---

## 🌐 访问 Web 控制台

启动后在浏览器打开：
`http://<设备IP>:8080`

首次打开按提示设置管理员初始密码，进入控制台可享受：
- 🚀 **实时网络吞吐与传输总量看板**：动态展示当前下载/上传速率（`B/s` ~ `MB/s`）与本次启动累计传输总量（带活跃传输微标）；
- 🎯 **客户端活跃优选节点 (Prefer Nodes)**：感知客户端携带优选 IP/Host，**实施规则 A 稳定排序**（默认兜底置顶 + 业务请求量从大到小排列，不跳动）；
- 📈 **后端健康与负载均衡**：实时存活检测、丢包率与业务成功率统计；
- ☁️ **Cloudflare 额度智能巡检**：官方 GraphQL API 直连 / UsagePanel 镜像额度实时监控与 95% 额度自动避让；
- 📊 **指标卡片多维呈现**：D1 SQL、R2 存储、KV 空间指标详情展示；
- 🔄 **Web 一键平滑自重启**：右上角【重启网关】一键延迟脱离自拉起（自动解绑端口后无缝接管）。

---

## 📖 全周期开发与运维记忆文档

本项目经历了从 Node.js 原始版本到 Go 高性能架构的大重构，包含大量 OpenWrt 部署排坑实战、流量防死循环机制与设计决策：
- 完整开发上下文与排坑记忆详见：[PROJECT_MEMORIES.md](PROJECT_MEMORIES.md)

---

## 🛡️ 与 Nikki (Mihomo / Clash.Meta) 同机协同避坑指南

如果您将 `edt-gateway` 与 **Nikki (luci-app-nikki)** 部署在同一台 OpenWrt 路由器上，必须设置**进程防环路绕行**，以避免透明代理死循环（导致 CPU 100% 或网络阻断）。

一键安装脚本已自动为您创建了专属非特权系统用户 **`edtgw`** (UID 888)，只需在 Nikki 界面做一步配置：

### 配置步骤（Nikki 访问控制白名单）
1. 登录 OpenWrt 网页后台，进入 **Nikki** -> **访问控制**；
2. 找到控制表格的**第一行**（即右侧 **【DNS】** 和 **【代理】** 均**未勾选**的直连放行规则行）；
3. 在【用户】下方的下拉框中，选择 **`edtgw`** 并点击添加；
4. 点击右下角 **【保存并应用】**。

> **原理说明**：经过此设置后，`edt-gateway` 发起连接外部 Cloudflare CDN 优选 IP 的流量将在 Linux 内核层直接打上 `edtgw` 标签被放行，**绝对不会被抓入透明代理环路**，彻底消除死循环风险。


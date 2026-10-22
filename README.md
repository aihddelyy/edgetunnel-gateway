# EdgeTunnel LB (Go 重构高性能版)

基于 Go 语言全新重写的 Cloudflare 边缘分流智能聚合网关。针对多子账户 Cloudflare Workers / Pages 免费配额深度优化，提供**协议嗅探自适应、128KB 握手首包无感故障转移、多账户 API 实时配额避让及 Web 可视化管理**。

---

## 🌟 核心特性与优势 (对比 Node.js 原版)

| 指标 / 特性 | Node.js 原版 | Go 重构高性能版 | 提升说明 |
| :--- | :--- | :--- | :--- |
| **内存常驻 (RSS)** | ~100MB - 250MB | **~15MB - 25MB** | **降低 85%+**，极大降低路由器与 NAS 负担 |
| **多核并发** | 依赖 Cluster 多进程 + IPC | **原生 Goroutine 调度** | 真正的单进程多核高并发，无 IPC 通信开销 |
| **交付形式** | 需 Node.js 运行时或大镜像 | **单静态二进制 (Single Binary)** | `//go:embed` 内嵌前端，零外部文件依赖 |
| **镜像体积** | ~180MB (Node Alpine) | **~18MB (Alpine) / ~12MB (Scratch)** | 极速拉取与构建 |
| **软路由适配** | OpenWrt 部署困难 | **原生支持 MIPS / ARM / x86_64 交叉编译** | 可直接作为 systemd 或 rc.local 后台进程运行 |

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
│       ├── web.go            # 8789 端口 REST API 与 SSE 接口
│       └── public/           # 内嵌的前端 Web 控制台静态资源 (HTML/CSS/JS)
├── Dockerfile                # 多阶段极简 Docker 镜像构建
├── docker-compose.yml        # Docker Compose 编排文件
├── config.example.json       # 配置示例
└── go.mod
```

---

## 🚀 编译与运行

### 方式一：Docker Compose（推荐）

```bash
cd go_version

# 首次启动前生成配置文件
cp config.example.json config.json

# 编译并后台启动
docker compose up -d --build
```

### 方式二：本地直接编译（Linux / macOS / Windows）

```bash
cd go_version

# 下载依赖
go mod download

# 本地编译
go build -ldflags="-s -w" -o edgetunnel-lb ./cmd/server

# 运行
./edgetunnel-lb -config config.json
```

### 方式三：软路由 / NAS 交叉编译

```bash
cd go_version

# 编译为 Linux ARM64 (适用于绝大多数主流软路由、树莓派等)
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o edgetunnel-lb-arm64 ./cmd/server

# 编译为 Linux AMD64 (适用于 x86 PVE / ESXi / 群晖 / 威联通)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o edgetunnel-lb-amd64 ./cmd/server
```

---

## 🌐 访问 Web 控制台

启动后在浏览器打开：
`http://<网关设备IP>:8789`
首次打开无需密码，按提示即可设置管理员初始密码并配置 Cloudflare 凭据。

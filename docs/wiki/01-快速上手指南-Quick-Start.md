# 🚀 快速上手指南 (Quick Start)

本文档将指导你在群晖 NAS、软路由或任何 Linux 服务器上从零完成 **EdgeTunnel Gateway** 的快速部署与首次运行。

---

## 📋 1. 前置环境与准备

在部署之前，请确认以下环境：

- **Docker 与 Docker Compose**：Docker 20.10+，支持 Compose V2；
- **系统硬件**：
  - CPU：x86_64 或 ARM64（推荐单核及以上）；
  - 内存：空闲内存 ≥ 128MB（网关常驻内存仅需约 50~80MB）；
- **网络模式（强烈推荐）**：
  - 推荐使用 **Docker Macvlan 网络** 赋予网关一个独立的局域网 IP（例如 `10.10.10.35`），避免与宿主机端口产生任何冲突，且方便内网全设备（软路由、PC、手机）直接作为直连目标。

---

## 🌐 2. 规划 Macvlan 独立网络 (可选但强烈推荐)

若你需要在局域网为网关分配一个纯净独立 IP（例如内网网段为 `10.10.10.0/24`，网关路由为 `10.10.10.1`，物理网卡为 `eth0`）：

在 Linux 或群晖 NAS SSH 下执行一次创建：
```bash
docker network create -d macvlan \
  --subnet=10.10.10.0/24 \
  --gateway=10.10.10.1 \
  -o parent=eth0 \
  mvlan
```
> [!TIP]
> 群晖 NAS 用户若有多物理网口（如 `ovs_eth0` 或 `eth0`），请通过 `ip addr` 命令确认主内网通信网卡名称并替换 `parent` 参数。

> [!IMPORTANT]
> **⚠️ 核心避坑：宿主机物理网卡必须启用混杂模式 (Promiscuous Mode)**
> Macvlan 网络机制会为 Docker 容器生成一个全新的独立虚拟 MAC 地址。默认情况下，Linux 宿主机的物理网卡若未开启混杂模式，硬件和驱动层会**直接丢弃所有“目标 MAC 不是宿主机物理网卡自身”的外来数据包**，导致局域网其他设备完全无法访问容器、出现严重丢包或连接时断时续！
>
> 1. **临时启用混杂模式（立即生效）**：
>    ```bash
>    sudo ip link set eth0 promisc on
>    ```
>    *(注：请将 `eth0` 替换为你实际的物理网卡名称，群晖通常为 `eth0` 或 `ovs_eth0`)*
> 
> 2. **群晖 NAS 开机自动持久化（防止重启失效）**：
>    群晖 NAS 系统重启后网卡混杂模式可能会被重置，强烈建议在【群晖控制面板】➔【任务计划】中新增一个【触发的任务】（事件：开机）：
>    - **任务名称**：`Enable_Promisc_Mode`
>    - **用户账号**：`root`
>    - **运行命令**：`ip link set eth0 promisc on`
> 
> 3. **ESXi / PVE 虚拟化环境特别提醒**：
>    若群晖或 Linux 运行在 ESXi 或 PVE 虚拟机中，**宿主机外层的虚拟交换机（vSwitch）必须勾选放行**：
>    - **ESXi**：在端口组安全设置中将 **【混杂模式】**、**【MAC 地址更改】** 和 **【伪造传输】** 均设为 **“接受 (Accept)”**；
>    - **PVE**：Linux Bridge 默认放行，但若开启了虚拟防火墙需允许相关 MAC/IP 流量。

---

## 🐳 3. 使用 Docker Compose 部署

### 步骤 1：创建项目工作目录
```bash
mkdir -p /volume2/docker/edgetunnel-lb
cd /volume2/docker/edgetunnel-lb
```

### 步骤 2：下载或编写 `docker-compose.yml`
创建 `docker-compose.yml` 文件：

```yaml
version: '3.8'

services:
  gateway:
    image: node:20-alpine
    container_name: edgetunnel-gateway
    restart: unless-stopped
    volumes:
      # 挂载宿主机目录（包含业务代码、静态资源与持久化配置）
      - ./:/app
    working_dir: /app
    # 核心：注入 --expose-gc 参数，解锁 V8 原生垃圾回收能力
    command: ["node", "--expose-gc", "server.mjs"]
    networks:
      mvlan:
        ipv4_address: 10.10.10.35  # 请指定局域网未被占用的静态 IP
    dns:
      - 223.5.5.5       # 阿里公共纯净 DNS
      - 119.29.29.29     # 腾讯公共 DNS
    logging:
      driver: "json-file"
      options:
        max-size: "10m"
        max-file: "3"
    environment:
      - TZ=Asia/Shanghai
      - PORT=8787                    # Pages / WebSocket 端口
      - H2C_PORT=8788                # Worker / XHTTP 自适应双模端口
      - WEB_PORT=8789                # Web UI 管理控制台端口
      - PREFERRED_SERVER=cf.example.com # 默认兜底优选域名或 IP
      - PAGES_BACKENDS=p1.example.com,p2.example.com
      - WORKER_BACKENDS=w1.example.com,w2.example.com
      - TRAFFIC_SAMPLE_MINUTES=5
      - CF_PANEL_URL=https://usagepanel.pages.dev
      # 内存守护参数
      - MEMORY_THRESHOLD_MB=150
      - MEMORY_INTERVAL_MINUTES=5
      # Telegram 告警配置 (可选)
      - TG_BOT_TOKEN=
      - TG_CHAT_ID=
      - TG_PROXY=socks5://10.10.10.1:7890

networks:
  mvlan:
    external: true
```

### 步骤 3：拉取代码并启动
确保 `/volume2/docker/edgetunnel-lb` 目录下包含网关源码文件（`server.mjs`、`lib/`、`public/` 等），随后执行：
```bash
docker compose up -d
```

### 步骤 4：查看启动日志
```bash
docker compose logs -f
```
看到如下初始化成功横幅，即代表网关各协议模块全部就绪：
```text
============================================================
🚀 EdgeTunnel 本地自适应分流网关已全部启动！(模块化架构版)
   🔌 WebSocket (HTTP/1.1) 监听端口: 8787
      • Pages 后端池: p1.example.com, p2.example.com
   ⚡ XHTTP 双模监听端口:    8788 (兼容 Mihomo H2 与 v2rayN H1)
      • Worker 后端池: w1.example.com, w2.example.com
   🌐 默认实际 Server:     cf.example.com
   🖥️ Web UI 管理控制台:    http://0.0.0.0:8789
   🧠 内存守护与自动回收: 已启用 (阈值: 150MB, 巡检: 每 5 分钟, 原生GC: 已就绪)
   📢 Telegram 告警通知:   已启用 (SOCKS5/代理: socks5://10.10.10.1:7890)
============================================================
```

---

## 🖥️ 4. 首次访问与密码初始化

1. 打开浏览器，访问控制台地址：
   ```text
   http://10.10.10.35:8789/
   ```
2. 首次进入系统将弹出 **“设置管理员初始密码”** 弹窗；
3. 输入你的管理员账号（默认 `admin`）与高强度密码，系统将使用 SHA-256 带随机盐（Salt）完成哈希存储；
4. 勾选 **“记住用户名与密码”**，下次打开页面即可实现自动识别与一键登录！

---

## 🔄 5. 日常维护与平滑更新

网关采用原生 ES Modules 零编译设计，配合宿主机卷挂载：
- **热更新逻辑**：当修改 `lib/`、`public/` 或 `server.mjs` 中的文件后，在控制台右上角点击 **“⚡ 重启服务”**（或调用 API `POST /api/restart`），进程将在 500ms 内平滑热退出并自动重新拉起生效，无须重新构建 Docker 镜像！

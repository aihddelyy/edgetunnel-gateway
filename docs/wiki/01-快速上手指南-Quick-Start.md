# 快速上手指南 (Quick Start)

本文档介绍在群晖 NAS、软路由或 Linux 服务器上部署并运行 **EdgeTunnel Gateway** 的完整流程。

---

## 1. 环境准备

- **Docker 与 Docker Compose**：Docker 20.10+，支持 Compose V2；
- **系统硬件**：
  - 架构：x86_64 或 ARM64；
  - 内存：建议空闲内存 ≥ 128MB（网关常驻内存约 50~80MB）；
- **网络模式建议**：
  - 推荐使用 **Docker Macvlan 网络** 为网关分配独立的局域网 IP（例如 `10.10.10.35`），避免端口与宿主机冲突，并方便局域网内所有设备统一指定网关地址。

---

## 2. 配置 Macvlan 网络 (推荐)

若需要为容器分配独立的内网 IP（示例：局域网网段 `10.10.10.0/24`，上游路由器 `10.10.10.1`，物理网卡 `eth0`）：

在宿主机执行以下命令创建 Macvlan 网络：
```bash
docker network create -d macvlan \
  --subnet=10.10.10.0/24 \
  --gateway=10.10.10.1 \
  -o parent=eth0 \
  mvlan
```
> **注意**：若宿主机有多个物理网卡（如群晖的 `ovs_eth0` 或 `eth0`），请通过 `ip addr` 确认主通信网卡名称并替换 `parent` 参数。

### 关键步骤：开启物理网卡混杂模式 (Promiscuous Mode)

Macvlan 会为容器生成独立的虚拟 MAC 地址。若物理网卡未开启混杂模式，驱动层通常会直接丢弃发往该虚拟 MAC 的数据包，导致局域网设备无法访问容器或出现丢包：

1. **临时启用（立即生效）**：
   ```bash
   sudo ip link set eth0 promisc on
   ```
   *(将 `eth0` 替换为实际网卡名)*

2. **群晖 NAS 开机自动启用**：
   系统重启后混杂模式可能被重置。可在【群晖控制面板】➔【任务计划】中新增【触发的任务】（开机触发）：
   - 任务名称：`Enable_Promisc_Mode`
   - 用户账号：`root`
   - 运行命令：`ip link set eth0 promisc on`

3. **虚拟机环境注意事项 (ESXi / PVE)**：
   - **ESXi**：在端口组安全策略中，将 **混杂模式**、**MAC 地址更改** 和 **伪造传输** 均设为 **“接受”**；
   - **PVE**：Linux Bridge 默认允许，若启用了防火墙需放行相关 MAC/IP 流量。

---

## 3. 使用 Docker Compose 部署

### 步骤 1：创建工作目录
```bash
mkdir -p /volume2/docker/edgetunnel-lb
cd /volume2/docker/edgetunnel-lb
```

### 步骤 2：编写 `docker-compose.yml`
在目录中创建 `docker-compose.yml`：

```yaml
version: '3.8'

services:
  gateway:
    image: node:20-alpine
    container_name: edgetunnel-gateway
    restart: unless-stopped
    volumes:
      - ./:/app
    working_dir: /app
    # 注入 --expose-gc 参数以启用 V8 主动垃圾回收
    command: ["node", "--expose-gc", "server.mjs"]
    networks:
      mvlan:
        ipv4_address: 10.10.10.35  # 指定局域网未占用的静态 IP
    dns:
      - 223.5.5.5
      - 119.29.29.29
    logging:
      driver: "json-file"
      options:
        max-size: "10m"
        max-file: "3"
    environment:
      - TZ=Asia/Shanghai
      - PORT=8787                    # Pages / WebSocket 端口
      - H2C_PORT=8788                # Worker / XHTTP 自适应端口
      - WEB_PORT=8789                # Web 控制台端口
      - PREFERRED_SERVER=cf.example.com # 默认兜底优选域名或 IP
      - PAGES_BACKENDS=p1.example.com,p2.example.com
      - WORKER_BACKENDS=w1.example.com,w2.example.com
      - TRAFFIC_SAMPLE_MINUTES=5
      - CF_PANEL_URL=https://usagepanel.pages.dev
      # 内存守护
      - MEMORY_THRESHOLD_MB=150
      - MEMORY_INTERVAL_MINUTES=5
      # Telegram 告警 (可选)
      - TG_BOT_TOKEN=
      - TG_CHAT_ID=
      - TG_PROXY=socks5://10.10.10.1:7890

networks:
  mvlan:
    external: true
```

### 步骤 3：启动容器
将项目源码放入该目录后，执行：
```bash
docker compose up -d
```

### 步骤 4：检查启动日志
```bash
docker compose logs -f
```
启动成功日志示例：
```text
============================================================
EdgeTunnel Gateway 已启动
   WebSocket 监听端口: 8787
      Pages 后端列表: p1.example.com, p2.example.com
   XHTTP 监听端口:    8788 (支持 H2C 与 H1.1)
      Worker 后端列表: w1.example.com, w2.example.com
   默认服务器:        cf.example.com
   Web 控制台:        http://0.0.0.0:8789
   内存守护状态:      已启用 (阈值: 150MB, 巡检: 每 5 分钟)
============================================================
```

---

## 4. 首次访问与密码初始化

1. 浏览器访问控制台地址：
   ```text
   http://10.10.10.35:8789/
   ```
2. 首次进入会弹出管理员初始密码设置窗口；
3. 输入管理员账号（默认 `admin`）与密码，系统将采用 SHA-256 带加盐（Salt）单向哈希后存入配置文件。

---

## 5. 日常维护与更新

- **代码修改与热重载**：由于项目源码通过卷挂载映射至容器内部，修改 `lib/`、`public/` 或 `server.mjs` 后，可在 Web 控制台点击右上角 **“重启服务”**（或调用 API `POST /api/restart`），主服务将在 500ms 内平滑重启生效，无需重建容器镜像。

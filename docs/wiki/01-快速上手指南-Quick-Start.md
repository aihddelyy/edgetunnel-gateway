# 快速上手

## 环境准备

- Docker 20.10+，支持 Compose V2。
- 宿主机：x86_64 或 ARM64 Linux（含群晖 NAS DSM 7+）。
- 网络：建议使用 Macvlan 模式分配独立局域网 IP，避免宿主机端口冲突。

---

## Macvlan 网络配置 (推荐)

假设局域网网段为 `10.10.10.0/24`，网关为 `10.10.10.1`，物理网卡为 `eth0`：

```bash
docker network create -d macvlan \
  --subnet=10.10.10.0/24 \
  --gateway=10.10.10.1 \
  -o parent=eth0 \
  mvlan
```

### 开启网卡混杂模式

Macvlan 为容器分配独立的虚拟 MAC 地址，物理网卡必须开启混杂模式，否则数据包会被驱动丢弃：

```bash
# 临时启用（立即生效）
sudo ip link set eth0 promisc on
```

若在群晖 NAS 上运行，可在【控制面板】➔【任务计划】中添加开机触发的 root 脚本：
```bash
ip link set eth0 promisc on
```

在 ESXi 等虚拟机环境中，需在虚拟交换机端口组安全策略中将“混杂模式”、“MAC 地址更改”和“伪造传输”均设为“接受”。

---

## 部署运行

### 1. 编写 `docker-compose.yml`

在工作目录（如 `/volume2/docker/edgetunnel-lb`）创建编排文件：

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
    command: ["node", "--expose-gc", "server.mjs"]
    networks:
      mvlan:
        ipv4_address: 10.10.10.35
    dns:
      - 223.5.5.5
      - 119.29.29.29
    environment:
      - TZ=Asia/Shanghai
      - PORT=8787
      - H2C_PORT=8788
      - WEB_PORT=8789
      - PREFERRED_SERVER=cf.example.com
      - PAGES_BACKENDS=p1.example.com,p2.example.com
      - WORKER_BACKENDS=w1.example.com,w2.example.com
      - MEMORY_THRESHOLD_MB=150

networks:
  mvlan:
    external: true
```

### 2. 启动服务

```bash
docker compose up -d
docker compose logs -f
```

启动输出包含各监听端口与后端列表即表示就绪。

---

## 控制台初始化

访问 `http://10.10.10.35:8789/`，首次打开按提示设置管理员用户名与密码，密码以加盐 SHA-256 保存至 `config.json`。

---

## 重启与更新

项目源码通过卷挂载运行。修改代码后，在 Web 控制台右上角点击“重启服务”，或执行：
```bash
curl -X POST http://10.10.10.35:8789/api/restart
```
服务将在 500ms 内平滑热重启，无需重建镜像。

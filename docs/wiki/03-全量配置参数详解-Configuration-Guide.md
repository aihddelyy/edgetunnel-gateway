# 配置参数

网关优先读取 Docker 环境变量作为默认基准；在 Web 控制台修改后，配置会持久化至挂载的 `config.json`，并支持历史快照备份与回滚。

---

## 环境变量 (`docker-compose.yml`)

| 变量名 | 默认值 | 说明 |
| :--- | :--- | :--- |
| `PORT` | `8787` | Pages / WebSocket 代理端口 |
| `H2C_PORT` | `8788` | Worker / XHTTP 双模代理端口 |
| `WEB_PORT` | `8789` | Web 管理控制台端口 |
| `PREFERRED_SERVER` | `cf.example.com` | 兜底优选服务器（客户端未指定 `prefer` 时使用） |
| `PAGES_BACKENDS` | - | Pages 真实后端域名列表（逗号分隔） |
| `WORKER_BACKENDS` | - | Worker 真实后端域名列表（逗号分隔） |
| `CF_PANEL_URL` | - | UsagePanel 镜像地址（用于额度监控） |
| `TG_BOT_TOKEN` | - | Telegram Bot Token |
| `TG_CHAT_ID` | - | Telegram Chat ID |
| `TG_PROXY` | - | Telegram 代理（如 `socks5://10.10.10.1:7890`） |
| `MEMORY_THRESHOLD_MB` | `150` | 内存自动回收阈值（MB） |
| `MEMORY_INTERVAL_MINUTES` | `5` | 内存巡检频率（分钟） |

---

## Web 控制台配置项

### 基础设置
- **兜底优选 Server**：当客户端请求未携带 `prefer` 参数时，出站连接的目标 IP 或域名。
- **流量采样间隔**：控制台顶部历史流量图的采样间隔（默认 5 分钟）。
- **后端域名列表**：逗号分隔的 Pages 与 Worker 域名，修改保存后即刻热重载，无需重启服务。

### 调度与额度
- **调度模式**：平权轮询 (Fair Round-Robin)。新请求严格均匀分配到各个健康节点。
- **额度统计口径**：
  - **单项统计 (默认)**：Worker 卡片统计 Worker 每日配额（上限 50,000 次），Pages 卡片统计 Pages 每日配额（上限 50,000 次）。
  - **账户总计**：统计关联子账户当天全部请求数（上限 100,000 次）。
- **配额避让阈值**：默认 95%（范围 10% ~ 100%）。当某个子账户当日请求消耗达到此比例时，网关自动停用该账户绑定的后端；当所有账户均超限时，降级为全量轮询以防断网。
- **域名与子账户映射**：为每个后端指定所属 Cloudflare 子账户，用于在节点卡片上展示对应账户的配额进度。

### 告警设置
- **Telegram 推送**：支持配置 Bot Token、Chat ID 与 SOCKS5 代理。在节点发生熔断或子账户配额超限时发送通知，内置 15 分钟频控。

---

## 配置备份与快照回滚

每次在 Web 控制台点击“保存配置”时，网关会在 `config.json` 同级目录自动生成带时间戳的备份文件：
```text
config.backup-2026-10-19T08-30-00.json
```
系统最多保留最近 20 份备份。在控制台右上角点击“配置历史”即可查看版本列表并一键回滚。

# 03. 全量配置参数详解 (Configuration Guide)

在 Go 原生架构中，网关使用极简、高效、规范的 JSON 格式持久化存储于工作目录下的 `config.json`（在 OpenWrt 部署下位于 `/etc/edt-gateway/config.json`）。

---

## 一、 `config.json` 标准格式范例

```json
{
  "port": 8080,
  "adminPassword": "sha256_hash_with_salt...",
  "defaultPrefer": "cf.090227.xyz:443",
  "backends": [
    {
      "domain": "pages-node-1.pages.dev",
      "type": "pages",
      "account": "user-a",
      "enabled": true
    },
    {
      "domain": "worker-node-1.workers.dev",
      "type": "worker",
      "account": "user-b",
      "enabled": true
    }
  ],
  "telegram": {
    "botToken": "",
    "chatId": "",
    "proxy": ""
  },
  "traffic": {
    "sampleIntervalSeconds": 5
  }
}
```

---

## 二、 核心字段详解

| 字段 | 类型 | 默认值 | 作用说明 |
| :--- | :--- | :--- | :--- |
| **`port`** | Integer | `8080` | Web 管理控制台与 RESTful API 监听端口 |
| **`adminPassword`** | String | `""` | 管理员认证凭据（首次登录在网页设定，安全加盐哈希固化） |
| **`defaultPrefer`** | String | `""` | 默认兜底优选服务器（客户端请求未携带 `prefer` 时作为全局兜底目标，格式 `IP:端口` 或 `域名:端口`） |
| **`backends`** | Array | `[]` | 边缘节点池列表（包含 Pages 与 Worker 节点配置） |
| `backends[].domain` | String | - | Cloudflare 分配的真实节点域名（如 `xxx.workers.dev` 或 `xxx.pages.dev`） |
| `backends[].type` | String | - | 节点类型，可选 `pages` 或 `worker` |
| `backends[].account`| String | - | 所属 Cloudflare 子账户标识（用于额度聚合与避让展示） |
| `backends[].enabled`| Boolean| `true`| 是否启用该节点参与轮询分流调度 |
| **`telegram`** | Object | - | 节点故障熔断告警与配额超限通知配置 |
| `telegram.botToken` | String | `""` | Telegram 官方机器人 Token |
| `telegram.chatId` | String | `""` | 接收告警通知的个人或频道 Chat ID |
| `telegram.proxy` | String | `""` | SOCKS5/HTTP 代理（如 `socks5://127.0.0.1:7890`） |

---

## 三、 控制台调度策略与额度避让

1. **平权均摊轮询调度 (Fair Round-Robin)**：
   - 客户端每一次发起新的连接，网关严格按列表轮换健康节点，使每一个绑定的子账户得到最为均匀的请求分配。
2. **额度避让熔断保护**：
   - 当某个子账户当天已用请求数达到避让阈值（默认 95%，即 47,500 / 50,000 次）时，网关将自动挂起该账户旗下的所有节点；
   - 其余未超限账户的节点继续承接流量；若所有子账户均超限，网关将降级全开以保障客户端网络不中断。

---

## 四、 自动快照备份与一键版本回滚

每次在 Web 控制台点击“保存配置”时，网关会在 `backups/` 目录下自动生成一份带有时间戳的配置镜像快照：
```text
backups/config.backup-2026-10-24T22-00-00.json
```
系统最多自动保留最近 20 份快照。若误修改了域名或误删了配置，可在控制台右上角点击“配置历史”快速比对并一键无损回滚。

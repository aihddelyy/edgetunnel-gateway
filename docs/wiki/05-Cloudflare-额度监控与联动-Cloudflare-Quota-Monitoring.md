# Cloudflare 额度监控与联动 (Cloudflare Quota Monitoring)

**EdgeTunnel Gateway** 支持出站分流与 **Cloudflare 配额监控** 联动。通过后台定时巡检（默认每 5 分钟），控制台能呈现各子账户当天的请求用量，并在配额接近上限时自动暂停向该账户对应的节点调度流量。

---

## 1. 监控模式对比

网关支持两种配额获取方式：

| 维度 | 模式 B: 官方 API 直连 (Direct，推荐) | 模式 A: UsagePanel 镜像模式 |
| :--- | :--- | :--- |
| **工作原理** | 直接通过 API Token 请求 Cloudflare 官方 GraphQL 接口 | 通过内置反向代理连接外部部署的 [UsagePanel](https://github.com/cmliu/CF-Workers-UsagePanel) |
| **部署依赖** | 无外部依赖 | 需事先部署并维护 UsagePanel 实例 |
| **数据源** | Cloudflare 官方接口实时获取 | 依赖 UsagePanel 面板自身的同步数据 |
| **凭据存储** | 凭据加密保存在本地配置中，不出局域网 | 需在网关中填写面板管理账号密码 |

---

## 2. 配置模式 B：官方 API 直连 (Direct)

### 步骤 1：获取 Account ID
1. 登录 [Cloudflare 控制台](https://dash.cloudflare.com/)；
2. 在任意域名或 Workers 概述页面右侧栏中复制 **Account ID**。

### 步骤 2：创建只读 API Token
1. 进入右上角头像 -> **My Profile (个人资料)** -> **API Tokens (API 令牌)**；
2. 点击 **Create Token (创建令牌)** -> 选择 **Custom Token (自定义令牌)**；
3. 填写令牌名称（例如 `edgetunnel-quota-read`）；
4. **配置只读权限 (Permissions)**：
   - `Account` -> `Account Analytics` -> `Read`
   - `Account` -> `Workers Scripts` -> `Read`
5. **Account Resources**：选择目标账户；
6. 生成并复制 API Token。

### 步骤 3：在控制台录入
1. 进入网关控制台 **“CF 额度面板”**；
2. 切换到 **“模式 B：Cloudflare 官方 API 直连”**；
3. 点击 **“添加账户凭据”**，填入账户别名、Account ID 与 API Token；
4. 点击 **“保存并测试直连”**，验证通过后后台开始定时巡检。

---

## 3. 配置模式 A：UsagePanel 镜像模式

若已部署 [CF-Workers-UsagePanel](https://github.com/cmliu/CF-Workers-UsagePanel)：
1. 进入网关控制台 **“CF 额度面板”**；
2. 切换到 **“模式 A：UsagePanel 镜像模式”**；
3. 填入面板访问地址（如 `https://usagepanel.pages.dev`）与登录凭据；
4. 保存后，网关将代理该面板页面，支持在内网直接内嵌查看。

---

## 4. 额度避让调度机制

- **触发条件**：某个子账户当天的请求用量达到设定阈值（默认 95%）；
- **调度动作**：网关在动态调度时自动将该账户绑定的 Worker / Pages 节点从健康可用池中剔除，后续请求分发至其余未超限账户；
- **全超限兜底**：若全部账户均达到阈值，网关会自动降级保留所有节点继续分发，避免网络中断。

---

## 5. Telegram 告警配置

支持在节点持续熔断或账户额度超限时发送通知：
1. **获取参数**：
   - 向 `@BotFather` 创建机器人获取 `TG_BOT_TOKEN`；
   - 获取接收通知的账号或群组 `TG_CHAT_ID`；
2. **内网代理设置**：
   - 若网关宿主机无法直接访问 Telegram API，可在控制台配置 `TG_PROXY`（如 `socks5://10.10.10.1:7890`）；
3. **频控策略**：内置 15 分钟节流限制，避免同一异常频繁告警。

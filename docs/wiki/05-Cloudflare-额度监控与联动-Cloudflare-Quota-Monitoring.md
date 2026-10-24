# 05. Cloudflare 额度监控与智能避让 (Cloudflare Quota Monitoring)

`edt-gateway` 后台定时（默认每 5 分钟）静默轮询 Cloudflare 免费配额，并在控制台实时呈现各子账户当日的使用比例与调用走势。当某子账户的用量达到设定阈值（默认 95%）时，网关将**自动对该账户旗下的全部节点执行软熔断避让**，流量无感平滑切换至其余健康子账户。

---

## 一、 两种监控模式对比

| 指标 | 模式 A：官方 GraphQL API 直连 (强烈推荐) | 模式 B：UsagePanel 镜像反向代理 |
| :--- | :--- | :--- |
| **技术实现** | 网关使用 API Token 直接调用 Cloudflare 官方 GraphQL Analytics API | 代理外置部署的 UsagePanel 前端面板 |
| **外部依赖** | **零依赖**，纯 Go 原生发起直接请求 | 需额外单独维护一套 UsagePanel 节点服务 |
| **数据安全性** | Token 保存在本地 `config.json`，纯内网闭环，绝不外泄 | 需在网关持久化第三方 UsagePanel 账号密码 |
| **稳定性** | 极高，直达 Cloudflare 核心接口，不受第三方网页变动影响 | 依赖第三方页面的 DOM 或反代稳定性 |

---

## 二、 模式 A：官方 API 直连配置实操

### 1. 获取 Account ID
在 [Cloudflare 控制台](https://dash.cloudflare.com/) 任意域名或 Workers 页面右侧栏复制您的 **Account ID**。

### 2. 创建只读 API Token
1. 进入右上角头像 -> **My Profile** -> **API Tokens**；
2. 点击 **Create Token** -> **Custom Token**；
3. 权限配置（必须且仅需只读权限）：
   - `Account` -> `Account Analytics` -> `Read`
   - `Account` -> `Workers Scripts` -> `Read`
4. 资源范围选择目标账户，创建并妥善复制该 Token。

### 3. 在网关控制台录入
1. 进入网关 Web 控制台 -> **“CF 额度面板”** -> **“官方 API 直连”**；
2. 点击“添加账户凭据”，填入子账户别名、Account ID 与 API Token；
3. 保存后网关立即发起连通性自测，测试成功后自动开始后台定时同步与避让联动。

---

## 三、 模式 B：UsagePanel 镜像配置

若您此前已部署了 [CF-Workers-UsagePanel](https://github.com/cmliu/CF-Workers-UsagePanel)：
1. 在控制台“CF 额度面板”中切换到“UsagePanel 镜像”；
2. 填入面板 URL（例如 `https://usagepanel.pages.dev`）及登录账号密码；
3. 保存后网关将通过内嵌反向代理安全呈现该面板的实时数据。

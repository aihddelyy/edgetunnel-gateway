# Cloudflare 额度监控

网关后台定时（默认 5 分钟）轮询 Cloudflare 配额，并在控制台展示各子账户当日使用量。当配额达到设定阈值（默认 95%）时，自动暂停向该账户对应的节点调度流量。

---

## 监控模式对比

| 模式 | 官方 API 直连 (推荐) | UsagePanel 镜像 |
| :--- | :--- | :--- |
| **原理** | 网关使用 API Token 直接调用 Cloudflare GraphQL API | 代理外部部署的 UsagePanel 面板 |
| **外部依赖** | 无 | 需单独运行 UsagePanel 服务 |
| **安全性** | Token 保存在本地 `config.json`，不外传 | 需在网关保存 UsagePanel 登录账号密码 |

---

## 模式 A：官方 API 直连配置

### 1. 获取 Account ID
在 [Cloudflare 控制台](https://dash.cloudflare.com/) 任意域名或 Workers 页面右侧栏复制 **Account ID**。

### 2. 创建只读 API Token
1. 进入右上角头像 -> **My Profile** -> **API Tokens**。
2. 点击 **Create Token** -> **Custom Token**。
3. 权限配置（只读）：
   - `Account` -> `Account Analytics` -> `Read`
   - `Account` -> `Workers Scripts` -> `Read`
4. 资源范围选择目标账户，创建并复制 Token。

### 3. 控制台录入
1. 进入网关控制台 **“CF 额度面板”** -> **“官方 API 直连”**；
2. 点击“添加账户凭据”，填入账户名称、Account ID 与 API Token；
3. 保存后网关立即发起连通性测试并开始定时同步。

---

## 模式 B：UsagePanel 镜像配置

若已部署 [CF-Workers-UsagePanel](https://github.com/cmliu/CF-Workers-UsagePanel)：
1. 在“CF 额度面板”中切换到“UsagePanel 镜像”；
2. 填入面板 URL（如 `https://usagepanel.pages.dev`）及登录账号密码；
3. 保存后网关将通过内嵌反代方式呈现该面板数据。

# 01. 快速上手指南 (Quick Start)

本项目采用 **Go 语言原生编写并静态编译为独立可执行二进制**，零运行时依赖。推荐直接部署在软路由系统（如 OpenWrt）或 Linux 物理机/虚拟机上，运行常驻内存仅需约 **15MB**。

---

## 🚀 方式一：OpenWrt 系统级一键全自动化安装 (强烈推荐)

在软路由 SSH 终端中执行以下一键命令，脚本将自动完成：**硬件架构自动匹配 (x86_64 / aarch64)、最新固件拉取、创建独立无特权系统用户 `edtgw`、部署 Procd 系统守护进程、注册开机自启、配置日志隔离与固件升级备份保护 (`sysupgrade.conf`)**：

```bash
# 官方原生直连
curl -sSL https://raw.githubusercontent.com/aihddelyy/edgetunnel-gateway/go_version/install.sh | sh

# 国内加速镜像 (推荐软路由环境使用)
curl -sSL https://ghfast.top/https://raw.githubusercontent.com/aihddelyy/edgetunnel-gateway/go_version/install.sh | sh
```

### 服务控制命令
```bash
/etc/init.d/edt-gateway start    # 启动服务
/etc/init.d/edt-gateway stop     # 停止服务
/etc/init.d/edt-gateway restart  # 重启服务
/etc/init.d/edt-gateway status   # 查看服务运行状态
```

---

## 💻 方式二：通用 Linux 物理机 / NAS 手动运行

1. 从 [GitHub Releases](https://github.com/aihddelyy/edgetunnel-gateway/releases) 下载适配当前平台的二进制文件：
   - 64 位 x86_64 主机：`edt-gateway-linux-amd64`
   - 64 位 ARM 主机：`edt-gateway-linux-arm64`
   - Windows 测试：`edt-gateway-windows-amd64.exe`

2. 重命名并赋予执行权限：
   ```bash
   mv edt-gateway-linux-amd64 edt-gateway
   chmod +x edt-gateway
   ```

3. 直接启动或使用 systemd 托管：
   ```bash
   # 默认工作目录运行，将自动在当前目录创建初始 config.json
   ./edt-gateway
   ```

---

## 🚪 默认端口规划

| 端口 | 协议类型 | 功能用途 |
| :--- | :--- | :--- |
| **8080** | HTTP / REST API | 现代化 Web 控制台与管理接口 |
| **8787** | WebSocket (WS) | Pages 节点专属分流代理 |
| **8788** | XHTTP (H2C / H1.1) | Worker 节点专属分流代理 (双模自适应) |

---

## 🖥️ 首次进入 Web 控制台

1. 浏览器访问：`http://你的路由器或主机IP:8080/`
2. 首次进入系统提示设置**管理员密码**，保存后密码自动使用安全加盐哈希固化到 `config.json`。
3. 点击顶部导航或节点管理，添加您的 Cloudflare Pages 与 Worker 边缘节点域名即可立即开始负载均衡调度。

---

## 🔄 在线平滑热更新与自重启

从 **v1.1.10** 起，网关内置了完全自愈的更新引擎：
* 无论何时有新版本发布，只需在 Web 控制台右上角点击 **“🚀 v1.x.x”** 打开更新模态窗。
* 支持通过内置加速镜像（`ghfast.top`、`ghproxy.net`）或**用户自定义镜像**极速拉取固件。
* 底层内置 **Anti-FakeIP 穿透 DNS**，即使网关与 Mihomo (Clash) 部署在同一主机且处于 Fake-IP 模式下，更新依然畅通无阻，下载完成后在 1 秒内执行文件原子替换并平滑自重启。

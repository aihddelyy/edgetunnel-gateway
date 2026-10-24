# edt-gateway 项目全周期架构演进与运维排坑记忆文档

> **文档定位**：本文件为项目长期演进的核心记忆库。记录了历次迭代、关键技术决策、OpenWrt 系统级部署规范、网络避坑指南以及不可违反的协作军规。

---

## 目录
1. [项目基础设施与仓库架构](#1-项目基础设施与仓库架构)
2. [协作军规与版本发布准则（核心约束）](#2-协作军规与版本发布准则核心约束)
3. [版本迭代演进与历次重大修改记录](#3-版本迭代演进与历次重大修改记录)
   - [v1.0.4：流光进度条与多层弹窗优化](#v104在线更新实时流光进度条与多层弹窗优化)
   - [v1.0.5：Linux/OpenWrt 进程平滑自重启深度修复](#v105linuxopenwrt-进程平滑自重启深度修复)
   - [v1.0.6：产物全面更名 edt-gateway 与双向平滑兼容](#v106产物全面更名-edt-gateway-与双向平滑兼容)
   - [v1.0.7：优选服务器请求量计数与活跃客户端感知](#v107优选服务器请求量计数与活跃客户端感知)
   - [v1.0.8：实时上下行速率与累计数据总量看板 (方案 B)](#v108实时上下行速率与累计数据总量看板-方案-b)
   - [v1.0.9：活跃优选服务器规则 A 稳定排序](#v109活跃优选服务器规则-a-稳定排序)
4. [OpenWrt 系统级部署与运维规范](#4-openwrt-系统级部署与运维规范)
   - [目录与文件布局规范](#目录与文件布局规范)
   - [Procd 守护进程与独立日志（绝不污染系统日志）](#procd-守护进程与独立日志绝不污染系统日志)
   - [固件升级（Sysupgrade）永续防丢失机制](#固件升级sysupgrade永续防丢失机制)
5. [同机透明代理（Nikki / OpenClash）防死循环避坑法则](#5-同机透明代理nikki--openclash防死循环避坑法则)
6. [一键全自动安装命令备忘](#6-一键全自动安装命令备忘)

---

## 1. 项目基础设施与仓库架构

### 1.1 双仓库结构
- **主目录/原仓库** (`local-edgetunnel-lb/`)：
  - 原始 Node.js 版本代码。
  - 分支名现已规范化更名为：`nodejs_version`。
- **Go 重构独立子仓库** (`go_version/`)：
  - 远程仓库：`https://github.com/aihddelyy/edgetunnel-gateway.git`。
  - **默认主分支（Default Branch）**：已切换为 **`go_version`**。
  - 交付产物：静态单二进制文件（`//go:embed` 内嵌前端 SPA），零外部依赖。

### 1.2 部署目标机约束
- **严禁向 NAS (`\\SA6400\docker\edgetunnel-lb`) 拷贝 Go 二进制**！
- 目标运行设备为 **OpenWrt 软路由 (10.10.10.6，Linux x86_64 / AMD64)**。

---

## 2. 协作军规与版本发布准则（核心约束）

> **【绝对版本控制规则】**：  
> **日常的代码开发、问题调试、局部优化或 Git 提交，必须严格保持当前版本号不变！**  
> **只有在用户明确下达“提升版本号 / 发布新版本 / 打 Tag 发布”的指令时，才允许修改版本号（`version.go`、`index.html`、`CHANGELOG.md`）、创建 Git Tag 并推送触发 Release 流水线！**

---

## 3. 版本迭代演进与历次重大修改记录

### v1.0.4：在线更新实时流光进度条与多层弹窗优化
- **背景/痛点**：点击更新后，底层系统版本弹窗与上层在线更新弹窗发生 z-index 冲突；缺乏实时下载网速反馈。
- **解决方案**：
  - 重构前端弹窗层级，引入高科技流光进度条。
  - 后端下载器接入流式计数中间件，提供免鉴权进度查询接口 `GET /api/update/progress`，实时计算已下载量、总大小、传输速率（如 `2.8 MB/s`）与百分比。

### v1.0.5：Linux/OpenWrt 进程平滑自重启深度修复
- **背景/痛点**：在 OpenWrt 软路由上通过 Web 界面点击“重启”或“更新后重启”时，网关退出后新进程无法拉起。
- **根因分析**：
  1. Linux 机制下，当旧可执行文件被原子重命名（`Rename`）并覆盖后，原进程的 `/proc/self/exe` 会被内核打上 ` (deleted)` 标签且已被 unlink；旧代码在替换后再次读取 `os.Executable()`，导致执行了一个不存在的文件路径。
  2. 嵌套子 Shell 语法 `/bin/sh -c "(sleep 2; ...) &"` 在 Busybox / ash 外层退出时可能被系统一并收回。
- **解决方案**：
  - 显式传递已替换完成的新程序绝对路径，并加入 `strings.TrimSuffix(..., " (deleted)")` 双重保险。
  - 改用独立 Session 进程直接运行 `sleep 2; cd '<dir>'; exec '<exe>' <args> >> app.log 2>&1`，无中间冗余 shell 残留。

### v1.0.6：产物全面更名 edt-gateway 与双向平滑兼容
- **统一产物命名规范**：
  - Linux AMD64：`edt-gateway-linux-amd64`
  - Linux ARM64：`edt-gateway-linux-arm64`
- **双向平滑兼容机制**：
  - 更新匹配引擎优先识别 `edt-gateway-{os}-{arch}`，同时向下兼容 `edgetunnel-lb-{os}-{arch}`。
  - 旧版本运行的网关可无缝识别并下载新版产物覆盖自身，实现跨版本零感知热升级。

### v1.0.7：优选服务器请求量计数与活跃客户端感知
- **背景/痛点**：控制台首页“客户端活跃优选服务器 (Prefer Nodes)”卡片中的请求量始终为 `0` 次，且来源设备始终显示为“网关自身”。
- **根因分析**：
  1. `PreferNode` 模型缺失 `requestCount` 与 `clients` 字段，前端取值为 `undefined` 导致显示为 0；
  2. 请求转发与节点探活未解耦，点击测速时也会误调用记录函数；
  3. `ws.go` 与 `xhttp.go` 转发时未提取真实客户端 IP（`r.RemoteAddr`）。
- **解决方案**：
  - `pkg/model/model.go`：补齐 `RequestCount int64` 与 `Clients []string`。
  - `pkg/prefer/prefer.go`：
    - `RecordRequest`：精准累加 `RequestCount++`，去端口提取纯 IP，最多保留 5 个最近活跃设备；
    - `RecordResponse`：仅更新延迟与成功/失败计数；
    - `RecordPing`：专供测速探活，**绝不虚增业务请求计数**；
    - `SetDefaultTarget`：独立维护默认兜底状态。
  - `pkg/proxy/ws.go` 与 `pkg/proxy/xhttp.go`：全链路在握手入站处埋点。

### v1.0.8：实时上下行速率与累计数据总量看板 (方案 B)
- **背景/需求**：用户需要在 Web 控制台实时查看当前网速吞吐与本次启动累计的上下行数据量。
- **解决方案**：
  - 新增核心模块 `pkg/traffic/traffic.go`：
    - 基于 `sync/atomic` 实现 `TotalRxBytes`（下载）与 `TotalTxBytes`（上传）零损耗原子计数；
    - 后台 1 秒滑动窗口平滑采样器，计算当前 `CurrentRxRate` 与 `CurrentTxRate`（Bps），并自适应转换单位。
    - 提供 `CountingWriter` 管道包装结构。
  - 全链路管道统计：
    - `ws.go` 的 `pipeConn`：`c1 -> c2`（上传）与 `c2 -> c1`（下载）分别接入 `CountingWriter`；
    - `xhttp.go`：客户端首包与 Body 写入远端累加上传，`resp.Body.Read` 写入客户端累加下载。
  - 前端顶部卡片（方案 B）：扩充为 6 列自适应网格，新增“🔻 实时下载速率”与“🔺 实时上传速率”卡片，带“传输中”高亮微标，轮询频率调至 2 秒。

### v1.0.9：活跃优选服务器规则 A 稳定排序
- **背景/痛点**：优选服务器卡片列表在刷新时先后顺序随机跳动闪烁（Go map 随机散列导致）。
- **解决方案（实施规则 A）**：
  1. **🌟 默认兜底服务器始终置顶（排在第 1 位）**；
  2. **其余节点严格按照业务请求量（`requestCount`）从大到小降序排列**；
  3. **请求量相同时按目标节点域名/IP 字典序保底**。
  - 后端在 `prefer.go` 的 `GetNodesList()` 中采用 `sort.SliceStable` 排序；
  - 前端在 `app.js` 的 `renderPreferNodes()` 中同样执行规则 A 排序做双重保险。

---

## 4. OpenWrt 系统级部署与运维规范

### 目录与文件布局规范
| 路径 | 权限/用户 | 说明 |
| :--- | :--- | :--- |
| `/usr/bin/edt-gateway` | `755 / root` | 系统全局命令，任何终端路径可直接执行 |
| `/etc/edt-gateway/config.json` | `644 / edtgw:edtgw` | 标准系统配置文件 |
| `/var/log/edt-gateway/app.log` | `644 / edtgw:edtgw` | 专属独立运行日志（tmpfs 内存盘） |
| `/etc/init.d/edt-gateway` | `755 / root` | OpenWrt 原生 Procd 系统服务脚本 |
| `/lib/upgrade/keep.d/edt-gateway` | `644 / root` | 固件升级保留名单规则文件 |

### Procd 守护进程与独立日志（绝不污染系统日志）
在 `/etc/init.d/edt-gateway` 中：
1. **进程守护**：设置 `procd_set_param respawn 3600 5 3`，异常崩溃 3 秒自动拉起自愈；
2. **专属非特权用户**：设置 `procd_set_param user edtgw` / `group edtgw`；
3. **彻底切断系统日志关联**：
   - 必须设置 `procd_set_param stdout 0` 与 `procd_set_param stderr 0`；
   - 输出重定向直接走 shell：`command /bin/sh -c "exec $PROG -config $CONF_FILE >> $LOG_FILE 2>&1"`；
   - 确保 `logread` 中完全不会有该程序的刷屏内容。
4. **生命周期原生托管**：严禁在 `USE_PROCD=1` 脚本中自定义 `stop_service()`，全权由 Procd 原生管理。

### 固件升级（Sysupgrade）永续防丢失机制
在 `/lib/upgrade/keep.d/edt-gateway` 中写入：
```text
/usr/bin/edt-gateway
/etc/edt-gateway/
/etc/init.d/edt-gateway
/etc/rc.d/S95edt-gateway
/lib/upgrade/keep.d/edt-gateway
```
> **自包含技巧**：将规则文件自身也写入清单，升级刷机后该规则自动复原，实现**每一次系统固件升级均 100% 自动保留并继续开机自启**。

---

## 5. 同机透明代理（Nikki / OpenClash）防死循环避坑法则

同机部署透明代理时，网关直连外部 Cloudflare 优选 IP 的出站连接若被代理内核抓回，会形成本地死循环（CPU 100%、网络瘫痪）。

### 5.1 Nikki (Mihomo) 配置法（最优雅）
一键脚本已创建了专属系统用户 **`edtgw`（UID: 888）**：
1. 打开 Nikki 网页后台 -> **【访问控制】**；
2. 找到表格第一行（右侧 **【DNS】** 与 **【代理】** 均**未勾选**的直连放行行）；
3. 在用户下拉菜单中选择 **`edtgw`** 并添加；
4. 点击 **【保存并应用】**。内核层直接识别 UID 放行，彻底杜绝回环。

### 5.2 OpenClash 配置法
在 OpenClash 网页后台 -> **【规则设置】** -> **【自定义规则】** 最顶部加入：
```yaml
- PROCESS-NAME,edt-gateway,DIRECT
```
点击保存并应用即可。

---

## 6. 一键全自动安装命令备忘

```bash
# 官方原生直连
curl -sSL https://raw.githubusercontent.com/aihddelyy/edgetunnel-gateway/go_version/install.sh | sh

# 国内加速镜像
curl -sSL https://ghfast.top/https://raw.githubusercontent.com/aihddelyy/edgetunnel-gateway/go_version/install.sh | sh
```

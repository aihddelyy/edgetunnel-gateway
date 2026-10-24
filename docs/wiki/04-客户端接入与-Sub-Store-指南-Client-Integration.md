# 04. 客户端接入与 Sub-Store 指南 (Client Integration)

客户端与 `edt-gateway` 之间的核心协议理念为：**内网明文传输（`tls: false`）+ 路径携带 `prefer=优选IP:端口` 参数**。真实的 TLS 1.3 / HTTP/2 握手由网关在公网出站阶段自动与 Cloudflare 边缘服务器建立。

---

## 一、 Mihomo (Clash Meta) 节点配置范例

在 Mihomo 配置文件的 `proxies` 列表中添加：

### 1. Worker (XHTTP / H2C) 节点 (连接 8788 端口)
```yaml
proxies:
  - name: "Edge-Worker-直连优选"
    type: vless
    server: 10.10.10.6        # 部署了 edt-gateway 的软路由局域网 IP
    port: 8788                # Worker 专属双模端口
    uuid: "your-uuid-here"
    network: xhttp
    tls: false                # 内网连接必须设为 false！
    udp: true
    xhttp-opts:
      mode: stream-up
      path: "/?prefer=cf.090227.xyz:443"  # 指定想直连的优选 IP 与端口
```

### 2. Pages (WebSocket) 节点 (连接 8787 端口)
```yaml
proxies:
  - name: "Edge-Pages-直连优选"
    type: vless
    server: 10.10.10.6        # 部署了 edt-gateway 的软路由局域网 IP
    port: 8787                # Pages 专属 WebSocket 端口
    uuid: "your-uuid-here"
    network: ws
    tls: false                # 内网连接必须设为 false！
    udp: true
    ws-opts:
      path: "/?prefer=104.16.80.1:443"     # 支持填写优选 IP 或域名
      headers:
        Host: "dummy.host"                 # 内网占位即可，网关出站自动重写为真实域名
```

---

## 二、 v2rayN (Xray-core) 节点配置范例

* **地址 (Address)**：`10.10.10.6` (网关局域网 IP)
* **端口 (Port)**：`8788` (Worker) 或 `8787` (Pages)
* **用户 ID (UUID)**：填写您的 EdgeTunnel 节点 UUID
* **传输协议 (Network)**：`xhttp` 或 `ws`
* **伪装类型 (Type)**：`none`
* **伪装域名 (Host)**：可留空或随意填写
* **路径 (Path)**：`/?prefer=cf.090227.xyz:443`
* **传输安全 (TLS)**：`none` (**切勿开启 TLS**，公网 TLS 由网关出站负责)

---

## 三、 Sub-Store 动态优选节点脚本转换

利用 Sub-Store 可以批量将原本直连 Cloudflare 域名的节点列表，转换为指向局域网 `edt-gateway` 负载均衡网关并附带多组不同优选 IP 的节点池。

### 脚本逻辑参考 (JavaScript)：
```javascript
function operator(proxies) {
  // 定义您测试表现最佳的一组优选 IP / 域名池
  const preferServers = [
    'cf.090227.xyz:443',
    '104.16.1.1:443',
    '162.159.192.1:443'
  ];

  const gatewayIP = '10.10.10.6';

  return proxies.map((p, index) => {
    const prefer = preferServers[index % preferServers.length];
    
    // 重定向至局域网网关
    p.server = gatewayIP;
    p.tls = false;

    if (p.network === 'ws') {
      p.port = 8787;
      p['ws-opts'] = p['ws-opts'] || {};
      p['ws-opts'].path = `/?prefer=${encodeURIComponent(prefer)}`;
    } else {
      p.port = 8788;
      p['xhttp-opts'] = p['xhttp-opts'] || {};
      p['xhttp-opts'].path = `/?prefer=${encodeURIComponent(prefer)}`;
    }

    p.name = `[EDT] ${p.name} -> ${prefer}`;
    return p;
  });
}
```
通过上述方式，无论客户端添加了多少个优选节点，所有流量均会汇总至 `edt-gateway` 统一执行平权轮询出站、熔断保护与实时流量统计。

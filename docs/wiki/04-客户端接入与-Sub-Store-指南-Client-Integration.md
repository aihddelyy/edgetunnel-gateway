# 客户端接入与 Sub-Store 指南 (Client Integration)

**EdgeTunnel Gateway** 作为局域网统一出口网关。内网客户端只需将原先直连 Cloudflare 公网的节点配置重定向至网关局域网 IP（如 `10.10.10.35`），即可由网关统一进行多域名轮询调度、优选 IP 出站与故障转移。

---

## 1. 客户端连接要求：为什么必须设置 `tls: false`？

配置客户端节点时，**必须将 TLS 关闭（`tls: false` 或 `security: none`）**：

- **连接对象**：客户端直连的是局域网内的网关设备（如 `10.10.10.35`），内网不存在公网证书；
- **加密位置**：TLS 1.3 / HTTP/2 加密建立在**网关到 Cloudflare 边缘服务器的出站链路**上，由网关携带真实的 Worker / Pages 域名完成 TLS 握手；
- **报错预防**：若客户端开启 TLS 直连网关，会导致局域网内证书校验失败（`certificate has expired` 或 `self-signed`）。

---

## 2. Sub-Store 节点自动转换脚本

使用项目自带的 `substore-operator.js` 脚本，可在 [Sub-Store](https://github.com/sub-store-org/Sub-Store) 中完成节点批量转换：

### 脚本处理流程
1. 提取节点原有的优选服务器地址与端口，转为 `prefer=ip:port` 拼接到 Path 末尾；
2. 将节点服务器地址指向网关局域网 IP（如 `10.10.10.35`）；
3. **端口分流**：WebSocket 节点分流至 `8787`，XHTTP 节点分流至 `8788`；
4. 将节点的 `tls` 设置为 `false`，清理多余斜杠，保留原有 Host、UUID 等认证参数。

### 配置方法
1. 打开 Sub-Store 界面；
2. 进入目标订阅（或组合订阅），添加 **Script Operator (脚本操作)**；
3. 将以下脚本代码粘贴至编辑器中（或直接引用仓库中的 `substore-operator.js`）；
4. 保存订阅后即可拉取已转换的节点。

```javascript
/**
 * Sub-Store 节点转换操作脚本
 */
function operator(proxies, targetPlatform) {
    const LOCAL_GATEWAY_IP = '10.10.10.35'; // 替换为你的网关局域网 IP
    const PORT_WS = 8787;
    const PORT_XHTTP = 8788;

    return proxies.map(proxy => {
        if (proxy.type && proxy.type.toLowerCase() === 'vless') {
            const originalServer = String(proxy.server || '').trim().replace(/\/+$/, '');
            const originalPort = proxy.port || 443;
            const prefer = originalPort === 443 ? originalServer : `${originalServer}:${originalPort}`;

            const isXhttp = (proxy.network && (proxy.network.toLowerCase() === 'xhttp' || proxy.network.toLowerCase() === 'splithttp')) ||
                            proxy['xhttp-opts'] || proxy['splithttp-opts'];

            proxy.server = LOCAL_GATEWAY_IP;
            proxy.port = isXhttp ? PORT_XHTTP : PORT_WS;

            proxy.tls = false;
            delete proxy.sni;
            delete proxy.ech;
            delete proxy.fingerprint;
            delete proxy.alpn;

            const appendPrefer = (p) => {
                if (!p) return `/?prefer=${encodeURIComponent(prefer)}`;
                const cleanP = p.replace(/\/+$/, '');
                return cleanP.includes('?') 
                    ? `${cleanP}&prefer=${encodeURIComponent(prefer)}` 
                    : `${cleanP}?prefer=${encodeURIComponent(prefer)}`;
            };

            if (proxy['ws-opts']) proxy['ws-opts'].path = appendPrefer(proxy['ws-opts'].path);
            if (proxy['xhttp-opts']) proxy['xhttp-opts'].path = appendPrefer(proxy['xhttp-opts'].path);
            if (proxy['splithttp-opts']) proxy['splithttp-opts'].path = appendPrefer(proxy['splithttp-opts'].path);
        }
        return proxy;
    });
}
```

---

## 3. 典型客户端配置示例

### ① Mihomo (Clash Meta)

#### XHTTP 节点 (端口 8788)
```yaml
proxies:
  - name: "[GW] 优选节点-XHTTP"
    type: vless
    server: 10.10.10.35     # 网关局域网 IP
    port: 8788              # XHTTP 监听端口
    uuid: 00000000-0000-0000-0000-000000000000
    udp: true
    tls: false              # 必须为 false
    network: xhttp
    xhttp-opts:
      host: lb.example.com  # 伪装 Host 保留
      path: /drama/html/album/proxyip=proxyip.example.com?ed=2560&prefer=cf.example.com
      mode: stream-one
      x-padding-obfs-mode: true
      x-padding-key: your_key
      x-padding-header: your_header
      x-padding-placement: queryInHeader
      x-padding-method: tokenish
    encryption: none
```

#### WebSocket 节点 (端口 8787)
```yaml
proxies:
  - name: "[GW] 优选节点-WS"
    type: vless
    server: 10.10.10.35     # 网关局域网 IP
    port: 8787              # WebSocket 监听端口
    uuid: 00000000-0000-0000-0000-000000000000
    udp: true
    tls: false              # 必须为 false
    network: ws
    ws-opts:
      path: /redirect.html/about/library/proxyip=104.16.1.1:443?prefer=104.16.1.1:443
      headers:
        Host: pglb.example.com
    encryption: none
```

### ② v2rayN (Xray-core)

在节点编辑页面中填写：
- **地址 (Address)**：`10.10.10.35`；
- **端口 (Port)**：`8788`（XHTTP）或 `8787`（WebSocket）；
- **用户 ID (UUID)**：你的 VLESS UUID；
- **传输协议 (Network)**：`xhttp` 或 `ws`；
- **传输安全 (TLS)**：`none`；
- **伪装域名 (Host)**：保留你的 Worker / Pages 原始域名；
- **路径 (Path)**：确保末尾携带 `?prefer=你的优选IP:端口`。

### ③ Sing-box

```json
{
  "type": "vless",
  "tag": "proxy-xhttp",
  "server": "10.10.10.35",
  "server_port": 8788,
  "uuid": "00000000-0000-0000-0000-000000000000",
  "transport": {
    "type": "xhttp",
    "path": "/?prefer=cf.example.com:443",
    "headers": {
      "Host": "lb.example.com"
    }
  }
}
```

# 客户端接入与 Sub-Store

客户端直连本地网关，因此节点配置必须将 TLS 关闭（`tls: false` 或 `security: none`）。真实的公网 TLS 1.3 / H2 加密由网关出站连接 Cloudflare 时完成。

---

## Sub-Store 转换脚本

在 [Sub-Store](https://github.com/sub-store-org/Sub-Store) 中为订阅添加 Script Operator 即可将订阅节点批量重定向至网关：

```javascript
function operator(proxies, targetPlatform) {
    const LOCAL_GATEWAY_IP = '10.10.10.35'; // 网关局域网 IP
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
                let path = p || '/';
                const hasQuery = path.includes('?');
                return `${path}${hasQuery ? '&' : '?'}prefer=${encodeURIComponent(prefer)}`;
            };

            if (isXhttp) {
                proxy['xhttp-opts'] = proxy['xhttp-opts'] || {};
                proxy['xhttp-opts'].path = appendPrefer(proxy['xhttp-opts'].path);
            } else {
                proxy['ws-opts'] = proxy['ws-opts'] || {};
                proxy['ws-opts'].path = appendPrefer(proxy['ws-opts'].path);
            }
        }
        return proxy;
    });
}
```

---

## 客户端配置示例

### Mihomo (Clash Meta)

#### XHTTP 节点
```yaml
proxies:
  - name: "Worker-XHTTP"
    type: vless
    server: 10.10.10.35
    port: 8788
    uuid: 00000000-0000-0000-0000-000000000000
    udp: true
    tls: false
    network: xhttp
    xhttp-opts:
      host: worker.example.com
      path: /drama/html/album/proxyip=proxyip.example.com?ed=2560&prefer=cf.example.com
      mode: stream-one
```

#### WebSocket 节点
```yaml
proxies:
  - name: "Pages-WS"
    type: vless
    server: 10.10.10.35
    port: 8787
    uuid: 00000000-0000-0000-0000-000000000000
    udp: true
    tls: false
    network: ws
    ws-opts:
      path: /redirect.html/about/library/proxyip=104.16.1.1:443?prefer=104.16.1.1:443
      headers:
        Host: pages.example.com
```

### v2rayN

在节点详情界面中设置：
- **地址 (Address)**：网关内网 IP（`10.10.10.35`）
- **端口 (Port)**：`8788`（XHTTP）或 `8787`（WS）
- **传输协议 (Network)**：`xhttp` 或 `ws`
- **传输安全 (TLS)**：`none`
- **路径 (Path)**：保留原有路径，并在末尾加上 `&prefer=优选IP:端口`

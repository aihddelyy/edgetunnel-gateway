import http from 'node:http';
import http2 from 'node:http2';
import net from 'node:net';
import { currentConfig } from './config.mjs';
import { getNextWorkerBackend, markBackendSuccess, markBackendFailure } from './health.mjs';
import { parseTargetHostPort, recordPreferActivity } from './prefer.mjs';
import { parsePreferAndCleanPath, getSafeHttp2ConnectUrl, isBenignError } from './utils.mjs';

const clientIpMap = new Map();
const h2SessionPool = new Map();
const MAX_SESSIONS_PER_TARGET = 4; // 每个目标后端最多维护 4 条并发 H2 会话
const MAX_STREAMS_PER_SESSION = 15; // 单条连接并发流阈值，超过时弹性开辟新会话
const IDLE_TIMEOUT_MS = 60000; // 空闲 60 秒自动清理回收

function getOrCreateH2Session(connectHost, connectPort, backendSni) {
  const poolKey = `${connectHost}:${connectPort}#${backendSni}`;
  let list = h2SessionPool.get(poolKey);
  if (!list) {
    list = [];
    h2SessionPool.set(poolKey, list);
  }

  // 1. 清理已无效的 session
  const validList = [];
  for (const item of list) {
    if (item.session && !item.session.destroyed && !item.session.closed && !item.session.socket?.destroyed) {
      validList.push(item);
    } else {
      try { item.session?.destroy(); } catch {}
    }
  }
  list = validList;
  h2SessionPool.set(poolKey, list);

  // 2. 寻找 activeStreams 最小的连接
  let bestItem = null;
  for (const item of list) {
    if (!bestItem || item.activeStreams < bestItem.activeStreams) {
      bestItem = item;
    }
  }

  // 3. 弹性判定：如果存在连接且并发未满阈值，直接复用
  if (bestItem && bestItem.activeStreams < MAX_STREAMS_PER_SESSION) {
    bestItem.lastUsed = Date.now();
    bestItem.activeStreams++;
    return { session: bestItem.session, isReused: true, poolKey, id: bestItem.id };
  }

  // 4. 并发流较高且当前连接数未达上限，弹性新建一条 H2 会话
  if (list.length < MAX_SESSIONS_PER_TARGET) {
    const targetUrl = getSafeHttp2ConnectUrl(connectHost, connectPort);
    const session = http2.connect(targetUrl, {
      servername: backendSni,
      rejectUnauthorized: false
    });

    const newItem = {
      id: Math.random().toString(36).substring(2, 9),
      session,
      poolKey,
      lastUsed: Date.now(),
      activeStreams: 1
    };
    list.push(newItem);

    const cleanup = () => {
      const currentList = h2SessionPool.get(poolKey);
      if (currentList) {
        const idx = currentList.findIndex(i => i.id === newItem.id);
        if (idx !== -1) currentList.splice(idx, 1);
        if (currentList.length === 0) h2SessionPool.delete(poolKey);
      }
    };

    session.on('error', cleanup);
    session.on('close', cleanup);
    session.on('goaway', cleanup);

    return { session, isReused: false, poolKey, id: newItem.id };
  }

  // 5. 已达最大连接数，复用并发流最少的一条
  if (bestItem) {
    bestItem.lastUsed = Date.now();
    bestItem.activeStreams++;
    return { session: bestItem.session, isReused: true, poolKey, id: bestItem.id };
  }

  // 6. 兜底新建
  const targetUrl = getSafeHttp2ConnectUrl(connectHost, connectPort);
  const session = http2.connect(targetUrl, {
    servername: backendSni,
    rejectUnauthorized: false
  });
  const fallbackItem = {
    id: Math.random().toString(36).substring(2, 9),
    session,
    poolKey,
    lastUsed: Date.now(),
    activeStreams: 1
  };
  list.push(fallbackItem);
  return { session, isReused: false, poolKey, id: fallbackItem.id };
}

function releaseH2Session(sessionWrapOrKey) {
  if (!sessionWrapOrKey) return;
  const poolKey = typeof sessionWrapOrKey === 'string' ? sessionWrapOrKey : sessionWrapOrKey.poolKey;
  const id = typeof sessionWrapOrKey === 'object' ? sessionWrapOrKey.id : null;
  const list = h2SessionPool.get(poolKey);
  if (!list) return;

  if (id) {
    const item = list.find(i => i.id === id);
    if (item) {
      item.activeStreams = Math.max(0, item.activeStreams - 1);
      item.lastUsed = Date.now();
    }
  } else if (list.length > 0) {
    const item = list.reduce((prev, curr) => (curr.activeStreams > prev.activeStreams ? curr : prev), list[0]);
    if (item) {
      item.activeStreams = Math.max(0, item.activeStreams - 1);
      item.lastUsed = Date.now();
    }
  }
}

export function cleanIdleH2Sessions(forceAllIdle = false) {
  let closedCount = 0;
  const now = Date.now();
  for (const [poolKey, list] of h2SessionPool.entries()) {
    const kept = [];
    for (const item of list) {
      const isIdle = item.activeStreams <= 0;
      if (isIdle && (forceAllIdle || (now - item.lastUsed > IDLE_TIMEOUT_MS))) {
        try {
          if (item.session && !item.session.destroyed) {
            item.session.destroy();
            closedCount++;
          }
        } catch {}
      } else {
        kept.push(item);
      }
    }
    if (kept.length === 0) {
      h2SessionPool.delete(poolKey);
    } else {
      h2SessionPool.set(poolKey, kept);
    }
  }
  return closedCount;
}

setInterval(() => {
  cleanIdleH2Sessions(false);
}, 15000);

function getRealClientIp(streamOrReq) {
  if (!streamOrReq) return '未知';
  const sock = streamOrReq.session?.socket || streamOrReq.socket || (streamOrReq.localPort !== undefined ? streamOrReq : null);
  if (!sock) return '未知';

  const realIp = clientIpMap.get(sock.remotePort) || clientIpMap.get(sock.localPort);
  return realIp || `${sock.remoteAddress || '未知'}:${sock.remotePort || ''}`;
}

export function createXhttpGateway(metrics, getCachedCfAccounts = () => null) {
  function handleWorkerForward(clientInStream, headers, rawPath, clientMethod, protoLabel, isHttp1Response = false, clientInReadable = null) {
    if (metrics) metrics.xhttpRequests++;
    const clientTag = getRealClientIp(clientInStream);
    const clientIp = (clientTag || '').split(':')[0].replace(/^\[|\]$/g, '');

    const { targetServer: extractedServer, cleanedPath } = parsePreferAndCleanPath(rawPath);
    const targetServer = extractedServer || currentConfig.defaultPrefer;
    const { host: connectHost, port: connectPort } = parseTargetHostPort(targetServer);

    recordPreferActivity(targetServer, clientTag);

    const inputReadable = (isHttp1Response && clientInReadable) ? clientInReadable : clientInStream;

    const baseHeaders = {
      ':method': clientMethod,
      ':path': cleanedPath,
      ':scheme': 'https'
    };

    for (const [key, value] of Object.entries(headers)) {
      if (!key.startsWith(':')) {
        const lower = key.toLowerCase();
        if (!['connection', 'keep-alive', 'transfer-encoding', 'upgrade', 'host', 'content-length'].includes(lower)) {
          if (lower === 'te' && value !== 'trailers') continue;
          baseHeaders[key] = value;
        }
      }
    }

    const initialChunks = [];
    let isHandshakeSuccess = false;
    let currentOutReq = null;
    let currentSessionWrap = null;
    let isClientEnded = false;

    const onClientData = (chunk) => {
      if (!isHandshakeSuccess && initialChunks.reduce((acc, c) => acc + c.length, 0) < 131072) {
        initialChunks.push(chunk);
      }
      if (currentOutReq && !currentOutReq.destroyed) {
        try {
          const canContinue = currentOutReq.write(chunk);
          if (!canContinue && typeof inputReadable.pause === 'function') {
            inputReadable.pause();
          }
        } catch {}
      }
    };

    const onClientEnd = () => {
      isClientEnded = true;
      if (currentOutReq && !currentOutReq.destroyed) {
        try { currentOutReq.end(); } catch {}
      }
    };

    inputReadable.on('data', onClientData);
    inputReadable.on('end', onClientEnd);

    const cleanupClient = () => {
      initialChunks.length = 0;
      inputReadable.removeListener('data', onClientData);
      inputReadable.removeListener('end', onClientEnd);
      if (currentOutReq) {
        try { currentOutReq.destroy(); } catch {}
      }
      if (currentSessionWrap) {
        releaseH2Session(currentSessionWrap);
        currentSessionWrap = null;
      }
    };

    clientInStream.on('close', cleanupClient);
    if (isHttp1Response && clientInReadable) {
      clientInReadable.on('close', cleanupClient);
    }

    const maxRetries = 2;
    const triedBackends = [];

    function attemptForwardXhttp(attempt = 0) {
      const isClientDead = isHttp1Response ? (clientInStream.writableEnded || clientInStream.destroyed) : (clientInStream.destroyed || clientInStream.closed);
      if (isClientDead) {
        cleanupClient();
        return;
      }

      const backendSni = getNextWorkerBackend(triedBackends, getCachedCfAccounts(), clientTag);
      triedBackends.push(backendSni);
      const remoteHostHeader = backendSni;

      const outHeaders = {
        ...baseHeaders,
        ':authority': backendSni
      };

      if (attempt === 0) {
        console.log(`------------------------------------------------------------\n` +
                    `[${protoLabel} #${clientTag}] 收到流式请求:\n` +
                    `  📥 [客户端入站 Ingress]\n` +
                    `     • 客户端来源: ${clientTag}\n` +
                    `     • 客户端 Host/Authority: ${headers[':authority'] || headers['host'] || '未指定'}\n` +
                    `     • 请求方式 Method: ${clientMethod}\n` +
                    `     • 客户端原始请求路径: ${rawPath}\n` +
                    `  📤 [网关转发出站 Egress]\n` +
                    `     • 实际 Server: ${connectHost}:${connectPort}\n` +
                    `     • 实际 TLS SNI: ${backendSni} (Worker后端)\n` +
                    `     • 实际 HTTP Host: ${remoteHostHeader}\n` +
                    `     • 实际请求路径: ${cleanedPath}\n` +
                    `------------------------------------------------------------`);
      } else {
        console.warn(`[XHTTP Failover #${clientTag}] 🔄 正在透明故障转移至备用 Worker 后端 (第 ${attempt} 次重试): ${backendSni}`);
      }

      let sessionWrap;
      try {
        sessionWrap = getOrCreateH2Session(connectHost, connectPort, backendSni);
      } catch (err) {
        if (!isBenignError(err)) {
          console.error(`[XHTTP 优选链路连接异常 #${clientTag} -> ${connectHost}:${connectPort}]: ${err.message} (交由客户端策略组自动切流)`);
          recordPreferActivity(targetServer, clientTag, null, 'error', err.message);
        }
        if (isHttp1Response) {
          if (!clientInStream.headersSent) clientInStream.writeHead(502);
          clientInStream.end();
        } else {
          try { clientInStream.close(http2.constants.NGHTTP2_INTERNAL_ERROR); } catch {}
        }
        cleanupClient();
        return;
      }

      currentSessionWrap = sessionWrap;
      const { session: cfSession, isReused } = sessionWrap;

      let outReq;
      try {
        outReq = cfSession.request(outHeaders);
        currentOutReq = outReq;
      } catch (err) {
        releaseH2Session(currentSessionWrap);
        currentSessionWrap = null;
        if (attempt < maxRetries) {
          attemptForwardXhttp(attempt + 1);
          return;
        }
        if (isHttp1Response) {
          if (!clientInStream.headersSent) clientInStream.writeHead(502);
          clientInStream.end();
        } else {
          try { clientInStream.close(http2.constants.NGHTTP2_INTERNAL_ERROR); } catch {}
        }
        cleanupClient();
        return;
      }

      for (const chunk of initialChunks) {
        try { outReq.write(chunk); } catch {}
      }
      if (isClientEnded) {
        try { outReq.end(); } catch {}
      }

      const startTime = Date.now();

      outReq.on('drain', () => {
        if (typeof inputReadable.resume === 'function') {
          inputReadable.resume();
        }
      });

      outReq.on('response', (inHeaders) => {
        const status = inHeaders[':status'];
        const elapsed = Date.now() - startTime;

        if (status === 200) {
          isHandshakeSuccess = true;
          initialChunks.length = 0;
          if (typeof inputReadable.resume === 'function') {
            inputReadable.resume();
          }
          markBackendSuccess(backendSni, elapsed);
          recordPreferActivity(targetServer, clientTag, elapsed, 'ok');
          console.log(`[${protoLabel} #${clientTag}] ✅ 握手成功: 状态码 200 (耗时: ${elapsed}ms, H2弹性连接复用: ${isReused ? '是' : '否'}) | 实际 Server: ${connectHost}:${connectPort} | SNI: ${backendSni}`);

          if (isHttp1Response) {
            const h1Headers = {};
            for (const [k, v] of Object.entries(inHeaders)) {
              if (!k.startsWith(':') && !['connection', 'keep-alive', 'transfer-encoding'].includes(k.toLowerCase())) {
                h1Headers[k] = v;
              }
            }
            clientInStream.writeHead(status, h1Headers);
            outReq.pipe(clientInStream);
          } else {
            clientInStream.respond(inHeaders);
            outReq.pipe(clientInStream);
          }
          return;
        }

        const canRetry = attempt < maxRetries && (status === 1101 || status === 429 || status === 502 || status === 503 || status === 403 || status === 521 || status === 522);
        if (canRetry) {
          markBackendFailure(backendSni, `HTTP 状态码: ${status}`, metrics);
          recordPreferActivity(targetServer, clientTag, elapsed, 'warn', `HTTP ${status}`);
          console.warn(`[${protoLabel} Failover #${clientTag}] ⚠️ 响应阻断状态码: ${status}，透明切换备用 Worker 重试...`);
          try { outReq.destroy(); } catch {}
          if (currentSessionWrap) {
            releaseH2Session(currentSessionWrap);
            currentSessionWrap = null;
          }
          attemptForwardXhttp(attempt + 1);
          return;
        }

        if (status >= 400) {
          markBackendFailure(backendSni, `HTTP 状态码: ${status}`, metrics);
          recordPreferActivity(targetServer, clientTag, elapsed, 'warn', `HTTP ${status}`);
        }

        if (isHttp1Response) {
          const h1Headers = {};
          for (const [k, v] of Object.entries(inHeaders)) {
            if (!k.startsWith(':') && !['connection', 'keep-alive', 'transfer-encoding'].includes(k.toLowerCase())) {
              h1Headers[k] = v;
            }
          }
          clientInStream.writeHead(status, h1Headers);
          outReq.pipe(clientInStream);
        } else {
          clientInStream.respond(inHeaders);
          outReq.pipe(clientInStream);
        }
      });

      outReq.on('error', (err) => {
        if (!isBenignError(err)) {
          console.warn(`[XHTTP 优选链路传输中断 #${clientTag} -> ${connectHost}:${connectPort}]: ${err.message} (交由客户端策略组自动切流)`);
          recordPreferActivity(targetServer, clientTag, null, 'error', err.message);
        }
        try { outReq.destroy(); } catch {}
        if (currentSessionWrap) {
          releaseH2Session(currentSessionWrap);
          currentSessionWrap = null;
        }
        if (isHttp1Response) {
          try { clientInStream.end(); } catch {}
        } else {
          try { clientInStream.close(http2.constants.NGHTTP2_INTERNAL_ERROR); } catch {}
        }
        cleanupClient();
      });

      outReq.on('close', () => {
        if (currentSessionWrap) {
          releaseH2Session(currentSessionWrap);
          currentSessionWrap = null;
        }
      });
    }

    attemptForwardXhttp(0);
  }

  const isolatedH2Server = http2.createServer();
  isolatedH2Server.on('stream', (stream, headers) => {
    if (metrics) metrics.h2cRequests++;
    handleWorkerForward(stream, headers, headers[':path'] || '/', headers[':method'] || 'POST', 'XHTTP/H2C', false);
  });

  const isolatedH1Server = http.createServer((req, res) => {
    if (metrics) metrics.h1Requests++;
    handleWorkerForward(res, req.headers, req.url || '/', req.method || 'POST', 'XHTTP/H1.1-POST', true, req);
  });

  return {
    listen(port, host, callback) {
      isolatedH2Server.listen(0, '127.0.0.1', () => {
        isolatedH1Server.listen(0, '127.0.0.1', () => {
          const h2InternalPort = isolatedH2Server.address().port;
          const h1InternalPort = isolatedH1Server.address().port;

          const mainGateway = net.createServer((socket) => {
            if (typeof socket.setNoDelay === 'function') socket.setNoDelay(true);
            const realClientAddress = (socket.remoteAddress || '未知') + ':' + (socket.remotePort || '');
            socket.on('error', (err) => {
              if (!isBenignError(err)) socket.destroy();
            });

            socket.once('data', (chunk) => {
              const isH2c = chunk.length >= 24 && chunk.toString('utf8', 0, 24) === 'PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n';
              const targetPort = isH2c ? h2InternalPort : h1InternalPort;

              const targetSocket = net.connect(targetPort, '127.0.0.1', () => {
                if (typeof targetSocket.setNoDelay === 'function') targetSocket.setNoDelay(true);
                clientIpMap.set(targetSocket.localPort, realClientAddress);
                targetSocket.write(chunk);
                socket.pipe(targetSocket);
                targetSocket.pipe(socket);
              });

              const cleanup = () => {
                if (targetSocket.localPort) clientIpMap.delete(targetSocket.localPort);
                try { targetSocket.destroy(); } catch {}
                try { socket.destroy(); } catch {}
              };

              targetSocket.on('close', cleanup);
              targetSocket.on('error', cleanup);
              socket.on('close', cleanup);
              socket.on('error', cleanup);
            });
          });

          mainGateway.listen(port, host, callback);
        });
      });
    }
  };
}

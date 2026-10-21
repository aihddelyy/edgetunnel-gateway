# 内存控制与性能调优 (Memory Guard & Performance)

在长时间运行代理服务、大文件连续传输或并发较多的场景下，任何对象滞留或缓冲区积压都可能导致物理内存占用上涨。**EdgeTunnel Gateway** 内置了多层内存控制机制，保证网关常驻物理内存（RSS）稳定在约 **50MB ~ 80MB** 范围内。

---

## 1. 内存消耗排查与控制要点

在代理服务开发中，常见的内存增长原因主要包括：

1. **V8 引擎默认垃圾回收策略**：
   - Node.js 默认根据宿主机总内存预设堆上限。在内存未触顶时，V8 较少主动触发 Major GC（标记-整理），且通常不会主动向操作系统归还空闲物理内存。
2. **首包暂存 Buffer 滞留**：
   - 故障转移需要在握手期在内存中暂存首包数据。若握手成功后未清空引用，长连接在整个传输过程中都会持有该 Buffer。
3. **缺乏 Stream 背压 (Backpressure)**：
   - 当客户端发送速率远高于远端处理速率时，若未根据底层套接字的写状态挂起读取流，未处理的数据会堆积在 Node.js 内存队列中。
4. **异常连接未彻底清理**：
   - 客户端异常断开时若未联动关闭远端连接，悬挂的套接字会长期占用内核缓冲区与句柄。

---

## 2. 内存控制核心实现

```text
客户端连接 ──► 暂存首包 (≤128KB) ──► 远端 200 握手成功 ──► 清空暂存 (initialChunks.length = 0)
                                            │
                                            ▼
持续转发 ────► 检测写缓冲区已满 ────► 暂停读取客户端输入 (pause) ──► 缓冲区排空恢复 (drain -> resume)
                                            │
                                            ▼
任一端断开 ──► 联动销毁对端 Socket ──► 释放文件描述符与底层 Buffer
```

### ① 握手即清机制
在 `lib/proxy-xhttp.mjs` 中，一旦远端返回 200 OK，网关立即清空首包暂存：
```javascript
isHandshakeSuccess = true;
initialChunks.length = 0; // 释放暂存数据
```

### ② Stream 背压流控 (Backpressure)
遵循标准 Stream 背压规范：
```javascript
const canContinue = currentOutReq.write(chunk);
if (!canContinue && typeof inputReadable.pause === 'function') {
  inputReadable.pause(); // 远端缓冲区满，暂停读取
}

outReq.on('drain', () => {
  if (typeof inputReadable.resume === 'function') {
    inputReadable.resume(); // 远端缓冲区排空后恢复
  }
});
```

### ③ Socket 联动销毁
客户端与远端任意一端触发 `close` 或 `error` 时，联动执行 `destroy()`，避免遗留悬挂连接：
```javascript
const cleanup = () => {
  if (outReq) try { outReq.destroy(); } catch {}
  if (clientSocket) try { clientSocket.destroy(); } catch {}
};
```

### ④ 后台巡检与 V8 原生 GC
在 `docker-compose.yml` 中启动参数加入 `--expose-gc`：
```yaml
command: ["node", "--expose-gc", "server.mjs"]
```
- 后台默认每 5 分钟巡检一次物理内存（RSS）；
- 超过设定阈值（默认 150MB）时，自动执行深度清理并调用 `global.gc()` 整理堆内存；
- 控制台顶部同时提供 **“立即回收”** 按钮，支持手动触发垃圾回收。

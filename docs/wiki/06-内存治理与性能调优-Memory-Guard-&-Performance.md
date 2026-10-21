# 内存治理与性能调优

在高吞吐转发场景下，网关通过以下四项机制将常驻物理内存（RSS）稳定在 50MB ~ 80MB 区间。

---

## 核心机制

### 1. 握手即清 (Handshake Buffer Release)
故障转移依赖在握手阶段暂存首包（最大 128KB）。一旦远端返回 200 OK，立即清空暂存引用，避免长连接持有 Buffer：

```javascript
isHandshakeSuccess = true;
initialChunks.length = 0; // 释放首包内存
```

### 2. Stream 背压控制 (Backpressure)
遵循 Node.js Stream 标准背压。当远端写入缓冲区满（`write()` 返回 `false`）时，暂停读取客户端输入流；缓冲区排空后（`drain` 事件）再恢复读取，防止数据在 Node.js 内存堆中排队积压：

```javascript
const canContinue = outReq.write(chunk);
if (!canContinue && typeof inputReadable.pause === 'function') {
  inputReadable.pause();
}

outReq.on('drain', () => {
  if (typeof inputReadable.resume === 'function') {
    inputReadable.resume();
  }
});
```

### 3. 连接联动销毁 (Socket Interlock Cleanup)
客户端或远端任意一端触发 `close`、`error` 或超时时，网关立即销毁对端 Socket 并清理内部会话句柄，防止悬挂连接泄漏套接字与缓冲区。

### 4. 主动垃圾回收 (V8 GC)
Node.js 默认较少向系统归还空闲内存。在容器启动命令中添加 `--expose-gc` 参数后，网关后台每 5 分钟巡检一次物理内存。若内存超过设定阈值（默认 150MB），自动执行全局清理并显式触发 `global.gc()`：

```yaml
command: ["node", "--expose-gc", "server.mjs"]
```

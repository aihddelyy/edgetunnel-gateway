package traffic

import (
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

// TrafficStats 流量统计结构体
type TrafficStats struct {
	TotalRxBytes     int64  `json:"totalRxBytes"`     // 累计总下载字节 (远端 -> 客户端)
	TotalTxBytes     int64  `json:"totalTxBytes"`     // 累计总上传字节 (客户端 -> 远端)
	CurrentRxRate    int64  `json:"currentRxRate"`    // 当前实时下载速率 (Bytes/s)
	CurrentTxRate    int64  `json:"currentTxRate"`    // 当前实时上传速率 (Bytes/s)
	TotalRxStr       string `json:"totalRxStr"`       // 格式化总下载量 (如 "2.45 GB")
	TotalTxStr       string `json:"totalTxStr"`       // 格式化总上传量 (如 "186.4 MB")
	CurrentRxRateStr string `json:"currentRxRateStr"` // 格式化下载速率 (如 "12.5 MB/s")
	CurrentTxRateStr string `json:"currentTxRateStr"` // 格式化上传速率 (如 "1.2 MB/s")
}

// Tracker 全局流量速率追踪器
type Tracker struct {
	totalRxBytes int64 // 下载总字节
	totalTxBytes int64 // 上传总字节

	currentRxRate int64 // 下载速率 B/s
	currentTxRate int64 // 上传速率 B/s

	lastRxBytes int64
	lastTxBytes int64
	lastTime    time.Time
}

var GlobalTracker = newTracker()

func newTracker() *Tracker {
	t := &Tracker{
		lastTime: time.Now(),
	}
	// 启动 1 秒级速率平滑采样器
	go t.startSampler()
	return t
}

func (t *Tracker) startSampler() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for now := range ticker.C {
		curRx := atomic.LoadInt64(&t.totalRxBytes)
		curTx := atomic.LoadInt64(&t.totalTxBytes)

		duration := now.Sub(t.lastTime).Seconds()
		if duration > 0 {
			diffRx := curRx - t.lastRxBytes
			diffTx := curTx - t.lastTxBytes

			if diffRx < 0 {
				diffRx = 0
			}
			if diffTx < 0 {
				diffTx = 0
			}

			rxRate := float64(diffRx) / duration
			txRate := float64(diffTx) / duration

			atomic.StoreInt64(&t.currentRxRate, int64(rxRate))
			atomic.StoreInt64(&t.currentTxRate, int64(txRate))

			t.lastRxBytes = curRx
			t.lastTxBytes = curTx
			t.lastTime = now
		}
	}
}

// RecordRx 累加下载字节 (下行流量)
func (t *Tracker) RecordRx(n int64) {
	if n > 0 {
		atomic.AddInt64(&t.totalRxBytes, n)
	}
}

// RecordTx 累加上传字节 (上行流量)
func (t *Tracker) RecordTx(n int64) {
	if n > 0 {
		atomic.AddInt64(&t.totalTxBytes, n)
	}
}

// GetStats 获取当前流量快照
func (t *Tracker) GetStats() TrafficStats {
	rx := atomic.LoadInt64(&t.totalRxBytes)
	tx := atomic.LoadInt64(&t.totalTxBytes)
	rxRate := atomic.LoadInt64(&t.currentRxRate)
	txRate := atomic.LoadInt64(&t.currentTxRate)

	return TrafficStats{
		TotalRxBytes:     rx,
		TotalTxBytes:     tx,
		CurrentRxRate:    rxRate,
		CurrentTxRate:    txRate,
		TotalRxStr:       FormatBytes(rx),
		TotalTxStr:       FormatBytes(tx),
		CurrentRxRateStr: FormatSpeed(rxRate),
		CurrentTxRateStr: FormatSpeed(txRate),
	}
}

// FormatBytes 将字节数格式化为易读文本
func FormatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	val := float64(b) / float64(div)
	switch exp {
	case 0:
		return fmt.Sprintf("%.1f KB", val)
	case 1:
		return fmt.Sprintf("%.2f MB", val)
	case 2:
		return fmt.Sprintf("%.2f GB", val)
	default:
		return fmt.Sprintf("%.2f TB", val)
	}
}

// FormatSpeed 将速率格式化为易读文本 (如 12.5 MB/s)
func FormatSpeed(bytesPerSec int64) string {
	return FormatBytes(bytesPerSec) + "/s"
}

// CountingWriter 包装 io.Writer 自动统计写入字节数
type CountingWriter struct {
	Writer  io.Writer
	OnWrite func(n int64)
}

func (cw *CountingWriter) Write(p []byte) (int, error) {
	n, err := cw.Writer.Write(p)
	if n > 0 && cw.OnWrite != nil {
		cw.OnWrite(int64(n))
	}
	return n, err
}

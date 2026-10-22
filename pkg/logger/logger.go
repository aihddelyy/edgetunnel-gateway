package logger

import (
	"fmt"
	"log"
	"sync"
	"time"

	"edgetunnel-lb/pkg/model"
)

const maxLogBuffer = 250

type Logger struct {
	mu          sync.RWMutex
	logs        []model.LogItem
	subscribers map[chan model.LogItem]struct{}
}

var globalLogger = &Logger{
	logs:        make([]model.LogItem, 0, maxLogBuffer),
	subscribers: make(map[chan model.LogItem]struct{}),
}

func GetLogger() *Logger {
	return globalLogger
}

func (l *Logger) appendLog(level, msg string) {
	now := time.Now()
	item := model.LogItem{
		ID:      now.UnixNano(),
		Time:    now.Format("15:04:05"),
		Level:   level,
		Message: msg,
	}

	l.mu.Lock()
	l.logs = append(l.logs, item)
	if len(l.logs) > maxLogBuffer {
		l.logs = l.logs[1:]
	}

	// Notify subscribers
	for ch := range l.subscribers {
		select {
		case ch <- item:
		default:
		}
	}
	l.mu.Unlock()

	// Print to console
	switch level {
	case "error":
		log.Printf("\033[31m[ERROR] %s\033[0m\n", msg)
	case "warn":
		log.Printf("\033[33m[WARN]  %s\033[0m\n", msg)
	default:
		log.Printf("[INFO]  %s\n", msg)
	}
}

func Infof(format string, args ...interface{}) {
	globalLogger.appendLog("info", fmt.Sprintf(format, args...))
}

func Warnf(format string, args ...interface{}) {
	globalLogger.appendLog("warn", fmt.Sprintf(format, args...))
}

func Errorf(format string, args ...interface{}) {
	globalLogger.appendLog("error", fmt.Sprintf(format, args...))
}

func (l *Logger) GetSnapshot() []model.LogItem {
	l.mu.RLock()
	defer l.mu.RUnlock()
	res := make([]model.LogItem, len(l.logs))
	copy(res, l.logs)
	return res
}

func (l *Logger) Subscribe() chan model.LogItem {
	ch := make(chan model.LogItem, 50)
	l.mu.Lock()
	l.subscribers[ch] = struct{}{}
	l.mu.Unlock()
	return ch
}

func (l *Logger) Unsubscribe(ch chan model.LogItem) {
	l.mu.Lock()
	delete(l.subscribers, ch)
	l.mu.Unlock()
	close(ch)
}

func (l *Logger) PruneLogs(keepCount int) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.logs) > keepCount {
		pruned := len(l.logs) - keepCount
		l.logs = l.logs[pruned:]
		return pruned
	}
	return 0
}

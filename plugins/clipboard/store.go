// 命令 main 提供剪贴板历史插件：内存里保留最近复制的文本，搜索后回车复制。
package main

import (
	"strings"
	"sync"
	"time"
)

// maxItems 是历史条数上限，超过后丢弃最旧的。
const maxItems = 50

// item 是一条剪贴板历史：文本与复制时间。
type item struct {
	Text string
	At   time.Time
}

// store 是按时间排序的历史，队首最新。所有方法并发安全。
type store struct {
	mu    sync.Mutex
	items []item
}

func newStore() *store { return &store{} }

// Add 记录一次复制：空白文本忽略；与最新一条相同忽略；与更早某条相同则
// 把它移到队首并刷新时间；否则插到队首，超过上限时丢弃最旧的。
func (s *store) Add(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, it := range s.items {
		if it.Text == text {
			if i == 0 {
				return
			}
			s.items = append(s.items[:i], s.items[i+1:]...)
			break
		}
	}
	s.items = append([]item{{Text: text, At: now}}, s.items...)
	if len(s.items) > maxItems {
		s.items = s.items[:maxItems]
	}
}

// Snapshot 返回最新在前的历史副本；filter 非空时只保留大小写不敏感包含它的条目。
func (s *store) Snapshot(filter string) []item {
	s.mu.Lock()
	defer s.mu.Unlock()
	filter = strings.ToLower(filter)
	out := make([]item, 0, len(s.items))
	for _, it := range s.items {
		if filter == "" || strings.Contains(strings.ToLower(it.Text), filter) {
			out = append(out, it)
		}
	}
	return out
}

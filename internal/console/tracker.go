package console

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Tracker はファイル転送の進捗を集計し、定期的に進捗行を描画する。
// nil レシーバでも全メソッドが安全に呼べる(進捗表示なしとして動く)。
type Tracker struct {
	p *Printer

	label      string
	totalFiles int64
	totalBytes int64

	doneFiles   atomic.Int64
	doneBytes   atomic.Int64
	failedFiles atomic.Int64

	mu      sync.Mutex
	stopCh  chan struct{}
	doneCh  chan struct{}
	running bool
}

// NewTracker は Printer に描画する Tracker を作る。
func NewTracker(p *Printer) *Tracker {
	return &Tracker{p: p}
}

// Start は進捗描画を開始する。
func (t *Tracker) Start(label string, totalFiles int, totalBytes int64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.running {
		return
	}
	t.label = label
	t.totalFiles = int64(totalFiles)
	t.totalBytes = totalBytes
	t.doneFiles.Store(0)
	t.doneBytes.Store(0)
	t.failedFiles.Store(0)
	t.stopCh = make(chan struct{})
	t.doneCh = make(chan struct{})
	t.running = true
	go t.loop(t.stopCh, t.doneCh)
}

func (t *Tracker) loop(stopCh, doneCh chan struct{}) {
	defer close(doneCh)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	t.render()
	for {
		select {
		case <-stopCh:
			return
		case <-tick.C:
			t.render()
		}
	}
}

func (t *Tracker) render() {
	done := t.doneFiles.Load()
	failed := t.failedFiles.Load()
	line := fmt.Sprintf("%s: %d/%d files  %s / %s",
		t.label, done+failed, t.totalFiles,
		HumanBytes(t.doneBytes.Load()), HumanBytes(t.totalBytes))
	if failed > 0 {
		line += fmt.Sprintf("  (%d failed)", failed)
	}
	t.p.SetProgress(line)
}

// FileDone は1ファイルの転送完了を記録する。
func (t *Tracker) FileDone(bytes int64) {
	if t == nil {
		return
	}
	t.doneFiles.Add(1)
	t.doneBytes.Add(bytes)
}

// FileFailed は1ファイルの転送失敗を記録する。
func (t *Tracker) FileFailed() {
	if t == nil {
		return
	}
	t.failedFiles.Add(1)
}

// Stop は進捗描画を終了し、進捗行を消す。何度呼んでもよい。
func (t *Tracker) Stop() {
	if t == nil {
		return
	}
	t.mu.Lock()
	if !t.running {
		t.mu.Unlock()
		return
	}
	t.running = false
	close(t.stopCh)
	doneCh := t.doneCh
	t.mu.Unlock()
	<-doneCh
	t.p.ClearProgress()
}

// HumanBytes はバイト数を人間向け表記にする。
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

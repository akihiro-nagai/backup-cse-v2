// Package console は端末出力(ログ行と進捗表示)の調停を行う。
package console

import (
	"fmt"
	"io"
	"os"
	"sync"
)

// Printer はログ行と進捗行が混ざらないように端末出力を直列化する。
// 端末(TTY)でない場合、進捗行は出力しない。
type Printer struct {
	mu       sync.Mutex
	w        io.Writer
	tty      bool
	progress string
}

// NewPrinter は f への Printer を作る。
func NewPrinter(f *os.File) *Printer {
	tty := false
	if fi, err := f.Stat(); err == nil {
		tty = fi.Mode()&os.ModeCharDevice != 0
	}
	return &Printer{w: f, tty: tty}
}

// Log は進捗行を一旦消してから1行出力し、進捗行を再描画する。
func (p *Printer) Log(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tty && p.progress != "" {
		fmt.Fprint(p.w, "\r\x1b[K")
	}
	fmt.Fprintln(p.w, line)
	if p.tty && p.progress != "" {
		fmt.Fprint(p.w, p.progress)
	}
}

// SetProgress は進捗行を更新する。
func (p *Printer) SetProgress(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if line == p.progress {
		return
	}
	p.progress = line
	if p.tty {
		fmt.Fprint(p.w, "\r\x1b[K", line)
	}
}

// ClearProgress は進捗行を消す。
func (p *Printer) ClearProgress() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tty && p.progress != "" {
		fmt.Fprint(p.w, "\r\x1b[K")
	}
	p.progress = ""
}

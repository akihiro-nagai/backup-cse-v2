// Package logging は slog ベースの二系統ロギングを提供する。
// 端末にはプレーンテキスト、ファイルには改行区切り JSON (NDJSON) を書く。
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"backup-cse/internal/console"
)

// New は端末(printer 経由)と NDJSON ファイルへ同時に記録する Logger を作る。
// jsonW が nil の場合は端末のみ。
func New(printer *console.Printer, jsonW io.Writer) *slog.Logger {
	handlers := []slog.Handler{newTermHandler(printer)}
	if jsonW != nil {
		handlers = append(handlers, slog.NewJSONHandler(jsonW, nil))
	}
	return slog.New(multiHandler(handlers))
}

// multiHandler は複数の slog.Handler へファンアウトする。
type multiHandler []slog.Handler

func (m multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var firstErr error
	for _, h := range m {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(multiHandler, len(m))
	for i, h := range m {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (m multiHandler) WithGroup(name string) slog.Handler {
	out := make(multiHandler, len(m))
	for i, h := range m {
		out[i] = h.WithGroup(name)
	}
	return out
}

// termHandler は "15:04:05 LEVEL message key=value" 形式で端末へ出力する。
type termHandler struct {
	printer *console.Printer
	attrs   []slog.Attr
}

func newTermHandler(p *console.Printer) *termHandler {
	return &termHandler{printer: p}
}

func (h *termHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelInfo
}

func (h *termHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Time.Format("15:04:05"))
	b.WriteByte(' ')
	b.WriteString(r.Level.String())
	b.WriteByte(' ')
	b.WriteString(r.Message)
	for _, a := range h.attrs {
		writeAttr(&b, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&b, a)
		return true
	})
	h.printer.Log(b.String())
	return nil
}

func writeAttr(b *strings.Builder, a slog.Attr) {
	fmt.Fprintf(b, " %s=%v", a.Key, a.Value.Any())
}

func (h *termHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := &termHandler{printer: h.printer}
	out.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return out
}

func (h *termHandler) WithGroup(string) slog.Handler {
	// グループはこの用途では使わないため無視する。
	return h
}

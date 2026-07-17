package logging

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"backup-cse/internal/console"
)

func TestLoggerWritesPlainAndNDJSON(t *testing.T) {
	// 端末側の出力をパイプ経由でキャプチャする。
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	printer := console.NewPrinter(w)

	var jsonBuf bytes.Buffer
	logger := New(printer, &jsonBuf)
	logger.Info("uploaded", "path", "a/b.txt", "size", 42)
	logger.Warn("something odd")
	w.Close()

	var termBuf bytes.Buffer
	if _, err := termBuf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	termOut := termBuf.String()
	if !strings.Contains(termOut, "INFO uploaded path=a/b.txt size=42") {
		t.Errorf("terminal output missing plain line: %q", termOut)
	}
	if !strings.Contains(termOut, "WARN something odd") {
		t.Errorf("terminal output missing warn line: %q", termOut)
	}
	// 端末出力は JSON ではない。
	if strings.Contains(termOut, `"msg"`) {
		t.Errorf("terminal output looks like json: %q", termOut)
	}

	// ファイル側は1行1JSON。
	lines := strings.Split(strings.TrimSpace(jsonBuf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("ndjson lines = %d: %q", len(lines), jsonBuf.String())
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("line 0 is not json: %v", err)
	}
	if rec["msg"] != "uploaded" || rec["path"] != "a/b.txt" {
		t.Errorf("ndjson record = %v", rec)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:             "0 B",
		512:           "512 B",
		1024:          "1.0 KiB",
		1536:          "1.5 KiB",
		1 << 20:       "1.0 MiB",
		3 << 30:       "3.0 GiB",
		1<<40 + 1<<39: "1.5 TiB",
	}
	for in, want := range cases {
		if got := console.HumanBytes(in); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

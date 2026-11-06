package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// codexCall is one request of a session file the test writes whole each
// time, as a session still at work grows.
type codexCall struct {
	respID        string
	at            time.Time
	input, output int
}

// writeCodexSession writes one Codex rollout holding calls, each with its
// turn.
func writeCodexSession(t *testing.T, path, id, session string, calls []codexCall) {
	t.Helper()
	lines := []any{map[string]any{"timestamp": "2026-09-30T10:00:00Z", "type": "session_meta", "payload": map[string]any{"id": id, "session_id": session, "source": "cli"}}}
	for _, c := range calls {
		lines = append(lines,
			map[string]any{"timestamp": c.at.Add(-time.Second).Format(time.RFC3339Nano), "type": "turn_context", "payload": map[string]any{"turn_id": "turn-" + c.respID, "model": "m", "effort": "high"}},
			map[string]any{"timestamp": c.at.Format(time.RFC3339Nano), "type": "token_usage_record", "payload": map[string]any{
				"response_id": c.respID, "session_id": session, "thread_id": id, "turn_id": "turn-" + c.respID,
				"usage": map[string]int{"input_tokens": c.input, "cached_input_tokens": 0, "cache_write_input_tokens": 0, "output_tokens": c.output, "reasoning_output_tokens": 0, "total_tokens": c.input + c.output},
			}})
	}
	var data []byte
	for _, line := range lines {
		b, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, b...)
		data = append(data, '\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

// A session still at work is made again at every page, and its chunk is
// new each time. The file of the chunks is written again once
// requestDiskLeast of them are made, and the chunks of one session file,
// made again over and over, counted as many: a cache of a long history was
// written whole again and again while one agent worked (#4276, 97.4 MB in
// 96 seconds). The threshold counts the session files that changed, not
// the chunks made.
func TestRequestDiskOneGrowingSessionWritesOnce(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	least, rows := requestDiskLeast, requestDiskLeastRows
	requestDiskLeast, requestDiskLeastRows = 3, 20000
	requestCache.Lock()
	requestCache.savedRows = false
	requestCache.Unlock()
	t.Cleanup(func() {
		requestDiskLeast, requestDiskLeastRows = least, rows
		requestCache.Lock()
		requestCache.savedRows = false
		requestCache.Unlock()
	})
	dir := filepath.Join(sessions.CodexDir(), "sessions", "2026", "09", "30")
	rollout := func(i int) string {
		return filepath.Join(dir, fmt.Sprintf("rollout-2026-09-30T10-00-0%d-s%d.jsonl", i, i))
	}
	calls := make([][]codexCall, 4)
	page := func() RequestPage {
		t.Helper()
		got := queryPage(All, Filter{}, 0, 100, sessions.ReadCallSource)
		requestDiskSaves.Wait()
		return got
	}
	// four session files: the first page makes a chunk of each, and writes
	// them (requestDiskLeast of them)
	for i := range 4 {
		calls[i] = []codexCall{{fmt.Sprintf("resp-%d", i), now.Add(-time.Duration(i+1) * time.Minute), 10, 1}}
		writeCodexSession(t, rollout(i), fmt.Sprintf("s%d", i), fmt.Sprintf("s%d", i), calls[i])
	}
	if p := page(); p.Total != 4 {
		t.Fatalf("first page: %d rows, want 4", p.Total)
	}
	written, err := os.ReadFile(requestDiskPath())
	if err != nil {
		t.Fatalf("4 chunks of 4 files made, and no file of them: %v", err)
	}
	// one of them grows, and grows again: its chunk is made again at every
	// page, more often than requestDiskLeast, and the file of them is not
	// written again, since only one session file changed
	later := time.Now().Add(time.Minute)
	for i := range 5 {
		calls[0] = append(calls[0], codexCall{fmt.Sprintf("resp-0-%d", i), now.Add(time.Duration(i) * time.Second), 100 + i, 1})
		writeCodexSession(t, rollout(0), "s0", "s0", calls[0])
		later = later.Add(time.Minute)
		os.Chtimes(rollout(0), later, later)
		if p := page(); p.Total != 4+i+1 {
			t.Fatalf("page %d: %d rows, want %d", i, p.Total, 4+i+1)
		}
	}
	if again, _ := os.ReadFile(requestDiskPath()); string(again) != string(written) {
		t.Fatalf("the file was written again for one session file made again 5 times")
	}
	// three session files changed: the file of their chunks is written
	for i := 1; i < 4; i++ {
		calls[i] = append(calls[i], codexCall{fmt.Sprintf("resp-%d-more", i), now.Add(time.Minute), 10, 1})
		writeCodexSession(t, rollout(i), fmt.Sprintf("s%d", i), fmt.Sprintf("s%d", i), calls[i])
		later = later.Add(time.Minute)
		os.Chtimes(rollout(i), later, later)
	}
	if p := page(); p.Total != 9+3 {
		t.Fatalf("page after three files changed: %d rows, want 12", p.Total)
	}
	if same, _ := os.ReadFile(requestDiskPath()); string(same) == string(written) {
		t.Fatalf("the file was not written for three session files made again")
	}
}

package runner

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestCollectorDeadlineClosesRetainedWriter(t *testing.T) {
	path, collector, cleanup, err := prepareEventCollector()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	defer collector.abort()
	writer, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := collector.finish(ctx); err == nil {
		t.Fatal("open event writer did not respect deadline")
	}
	if collector.reader != nil {
		t.Fatal("timed-out collector retained its reader")
	}
}

func TestPrivateEventPayloadHasATotalSizeLimit(t *testing.T) {
	if _, err := collectEventPayload(bytes.NewReader(make([]byte, runnerOutputLimit+1))); err == nil {
		t.Fatal("oversized private event payload accepted")
	}
	if payload, err := collectEventPayload(bytes.NewReader([]byte("small"))); err != nil || string(payload) != "small" {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
}

func TestTimeoutStopsDescendantsHoldingOutputOpen(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "late-child-write")
	executable := filepath.Join(root, "test-process")
	script := "#!/bin/sh\n(sleep 2; printf survived > " + strconv.Quote(marker) + ") &\nwait\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, _, timedOut, err := runGoTest(context.Background(), Request{
		ProjectRoot: root, GoBinary: executable, Timeout: 150 * time.Millisecond,
	}, []string{"PATH=/usr/bin:/bin"})
	if err != nil || !timedOut {
		t.Fatalf("timedOut=%v err=%v", timedOut, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("timeout waited for a descendant: %s", elapsed)
	}
	// A surviving descendant would write after two seconds, even after Run returned.
	time.Sleep(time.Until(started.Add(2200 * time.Millisecond)))
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("descendant survived cancellation: %v", err)
	}
}

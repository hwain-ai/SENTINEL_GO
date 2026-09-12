package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLifecycleRunnerSignalHelper(t *testing.T) {
	if os.Getenv("SENTINEL_RUNNER_SIGNAL_HELPER") != "1" {
		return
	}
	for index, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{os.Args[0]}, os.Args[index+1:]...)
			main()
			return
		}
	}
}

func TestLifecycleRunnerSignalStopsSeparateTestGroup(t *testing.T) {
	project := runnerCommandFixture(t)
	marker := filepath.Join(t.TempDir(), "pid")
	testPath := filepath.Join(project, "value_test.go")
	source := fmt.Sprintf("package command\nimport (\"testing\"; \"os\"; \"strconv\"; \"time\")\nfunc TestValue(t *testing.T) { os.WriteFile(%q, []byte(strconv.Itoa(os.Getpid())), 0600); time.Sleep(3*time.Second); if Value()!=1 {t.Fail()} }\n", marker)
	writeRunnerCommandFile(t, testPath, source)
	args := append([]string{"-test.run=^TestLifecycleRunnerSignalHelper$", "--"}, runnerArguments(project)...)
	command := exec.Command(os.Args[0], args...)
	command.Env = append(os.Environ(), "SENTINEL_RUNNER_SIGNAL_HELPER=1")
	var stdout bytes.Buffer
	command.Stdout = &stdout
	done := startRunnerSignalFixture(t, command, marker)
	deadline := time.Now().Add(15 * time.Second)
	var pid int
	for time.Now().Before(deadline) {
		payload, err := os.ReadFile(marker)
		if err == nil {
			pid, _ = strconv.Atoi(string(payload))
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid < 1 {
		t.Fatal("test helper did not start")
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runner shutdown unbounded")
	}
	if !runnerFixtureStopped(pid) {
		t.Error("separate test group still running before fallback cleanup")
	}
	after, err := os.ReadFile(testPath)
	if err != nil || string(after) != source {
		t.Error("test instrumentation not restored")
	}
	if stdout.Len() != 0 {
		t.Errorf("partial report: %q", stdout.String())
	}
}

func runnerFixtureStopped(pid int) bool {
	deadline := time.NewTimer(200 * time.Millisecond)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		state, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if os.IsNotExist(err) || (err == nil && strings.Contains(string(state), ") Z ")) {
			return true
		}
		select {
		case <-deadline.C:
			return false
		case <-tick.C:
		}
	}
}

func startRunnerSignalFixture(t *testing.T, command *exec.Cmd, marker string) <-chan struct{} {
	t.Helper()
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = time.Second
	done := make(chan struct{})
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	// Register before any fixture polling or assertion can exit the test.
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Error("fixture was not reaped after fallback")
				}
			}
		}
		cleanupRunnerFixturePID(t, marker)
	})
	go func() { _ = command.Wait(); close(done) }()
	return done
}

func cleanupRunnerFixturePID(t *testing.T, marker string) {
	t.Helper()
	payload, err := os.ReadFile(marker)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Error(err)
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(payload)))
	if err != nil || pid < 1 {
		t.Errorf("invalid fixture PID %q", payload)
		return
	}
	if !runnerFixtureStopped(pid) {
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Error(err)
		}
	}
}

func TestLifecycleRunnerFixtureCleanupOnEarlyReturn(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pid")
	var command *exec.Cmd
	var done <-chan struct{}
	t.Run("leave while helper runs", func(t *testing.T) {
		command = exec.Command("/usr/bin/bash", "-c", "echo $$ > \"$1\"; exec /usr/bin/sleep 2", "fixture", marker)
		done = startRunnerSignalFixture(t, command, marker)
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(marker); err == nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("fixture did not start")
	})
	select {
	case <-done:
	default:
		t.Fatal("early return did not wait/reap helper")
	}
	if command.ProcessState == nil || !runnerFixtureStopped(command.Process.Pid) {
		t.Fatal("early return left helper running")
	}
}

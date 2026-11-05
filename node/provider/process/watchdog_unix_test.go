//go:build darwin || linux

package providerprocess

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	watchdogHostFixtureEnvironment  = "TEAMVA_TEST_WATCHDOG_HOST_FIXTURE"
	watchdogChildFixtureEnvironment = "TEAMVA_TEST_WATCHDOG_CHILD_FIXTURE"
	watchdogLeaderPIDEnvironment    = "TEAMVA_TEST_WATCHDOG_LEADER_PID"
	watchdogHelperPIDEnvironment    = "TEAMVA_TEST_WATCHDOG_HELPER_PID"
	watchdogChildPIDEnvironment     = "TEAMVA_TEST_WATCHDOG_CHILD_PID"
)

func TestProcessTrackerCapacityCountsLiveIdentitiesNotHistory(t *testing.T) {
	tracker := newProcessTracker(1000, "test-lease")
	const limit = 2
	leader := watchedProcess{Identity: processIdentity{PID: 10, BirthTime: 1}, HasLease: true}
	for generation := 0; generation < 20; generation++ {
		// Only two processes are alive at a time, but more than the limit
		// have existed. The newest detached child must always be tracked.
		child := watchedProcess{
			Identity: processIdentity{PID: 100 + generation, BirthTime: uint64(2 + generation)},
			ParentID: 1, HasLease: true,
		}
		if alive := tracker.observeSnapshot([]watchedProcess{leader, child}, limit); alive != 2 {
			t.Fatalf("generation %d: alive=%d", generation, alive)
		}
		if _, tracked := tracker.known[child.Identity]; !tracked {
			t.Fatalf("generation %d: new setsid descendant was not tracked", generation)
		}
		if len(tracker.known) != limit || len(tracker.records) != limit {
			t.Fatal("tracker accumulated historical process records")
		}
		// Once discovered, clearing the inherited environment and reparenting
		// must not lose a still-live child with the same birth identity.
		child.HasLease = false
		if alive := tracker.observeSnapshot([]watchedProcess{leader, child}, limit); alive != 2 {
			t.Fatal("live detached child was forgotten")
		}
	}
	if alive := tracker.observeSnapshot(nil, limit); alive != 0 || len(tracker.records) != 0 || len(tracker.known) != 0 {
		t.Fatal("empty snapshot retained dead identities")
	}
}

func TestProcessTrackerPrunesReusedPIDBeforeFollowingAncestry(t *testing.T) {
	tracker := newProcessTracker(1000, "test-lease")
	root := watchedProcess{Identity: processIdentity{PID: 10, BirthTime: 1}, HasLease: true}
	tracker.observeSnapshot([]watchedProcess{root}, 3)
	reused := watchedProcess{Identity: processIdentity{PID: 10, BirthTime: 20}}
	unrelated := watchedProcess{Identity: processIdentity{PID: 11, BirthTime: 21}, ParentID: 10}
	if alive := tracker.observeSnapshot([]watchedProcess{reused, unrelated}, 3); alive != 0 {
		t.Fatal("reused PID inherited ownership or conferred it on an unrelated child")
	}
	reused.HasLease = true
	if alive := tracker.observeSnapshot([]watchedProcess{unrelated, reused}, 3); alive != 2 {
		t.Fatal("fresh lease identity and its live descendant were not discovered")
	}
	if _, stale := tracker.known[root.Identity]; stale {
		t.Fatal("retained an old birth identity after PID reuse")
	}
}

func TestProcessTrackerRejectsOlderChildFromMixedTimeSnapshot(t *testing.T) {
	tracker := newProcessTracker(1000, "test-lease")
	root := watchedProcess{Identity: processIdentity{PID: 10, BirthTime: 20}, HasLease: true}
	old := watchedProcess{Identity: processIdentity{PID: 11, BirthTime: 10}, ParentID: 10}
	child := watchedProcess{Identity: processIdentity{PID: 12, BirthTime: 21}, ParentID: 10}
	grandchild := watchedProcess{Identity: processIdentity{PID: 13, BirthTime: 22}, ParentID: 12}
	if alive := tracker.observeSnapshot([]watchedProcess{old, grandchild, child, root}, 4); alive != 3 {
		t.Fatalf("multi-pass ancestry discovery: alive=%d", alive)
	}
	if _, tracked := tracker.known[old.Identity]; tracked {
		t.Fatal("child predating reused parent PID was claimed")
	}
}

func TestProcessTrackerReclaimsCapacityAfterSaturation(t *testing.T) {
	tracker := newProcessTracker(1000, "test-lease")
	first := watchedProcess{Identity: processIdentity{PID: 10, BirthTime: 1}, HasLease: true}
	second := watchedProcess{Identity: processIdentity{PID: 11, BirthTime: 2}, HasLease: true}
	third := watchedProcess{Identity: processIdentity{PID: 12, BirthTime: 3}, HasLease: true}
	unrelated := watchedProcess{Identity: processIdentity{PID: 20, BirthTime: 4}}
	tracker.observeSnapshot([]watchedProcess{first, second, third, unrelated}, 2)
	if len(tracker.known) != 2 || len(tracker.records) != 2 {
		t.Fatal("active tracking limit was not respected")
	}
	if alive := tracker.observeSnapshot([]watchedProcess{second, third, unrelated}, 2); alive != 2 {
		t.Fatalf("capacity not reclaimed after tracked process exit: alive=%d", alive)
	}
	if _, tracked := tracker.known[third.Identity]; !tracked {
		t.Fatal("new detached process could not use reclaimed capacity")
	}
	if _, tracked := tracker.records[unrelated.Identity]; tracked {
		t.Fatal("unrelated same-UID process consumed tracked record capacity")
	}
}

func TestWatchdogProxiesProviderStdio(t *testing.T) {
	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		_ = stdinReader.Close()
		_ = stdinWriter.Close()
		t.Fatal(err)
	}
	command := exec.Command("/bin/sh", "-c", `IFS= read -r line; printf 'reply:%s\n' "$line"`)
	command.Stdin, command.Stdout = stdinReader, stdoutWriter
	tree, err := Start(command)
	_ = stdinReader.Close()
	_ = stdoutWriter.Close()
	if err != nil {
		_ = stdinWriter.Close()
		_ = stdoutReader.Close()
		t.Fatal(err)
	}
	if _, err := stdinWriter.WriteString("rpc-probe\n"); err != nil {
		t.Fatal(err)
	}
	_ = stdinWriter.Close()
	line, err := bufio.NewReader(stdoutReader).ReadString('\n')
	_ = stdoutReader.Close()
	if err != nil || line != "reply:rpc-probe\n" {
		t.Fatalf("stdio reply=%q err=%v", line, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := tree.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestWatchdogHelperLaunchImageExcludesProviderSecrets(t *testing.T) {
	secret := "watchdog-private-launch-image-probe"
	command := exec.Command("/bin/sh", "-c", secret)
	command.Env = []string{"PROVIDER_CREDENTIAL=" + secret}
	_, _, release, err := configureCommand(command)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, value := range append(append([]string(nil), command.Args...), command.Env...) {
		if strings.Contains(value, secret) {
			t.Fatal("Provider argument or credential leaked into watchdog argv/environment")
		}
	}
	if len(command.ExtraFiles) < 3 || command.Env[0] != watchdogModeEnvironment+"=1" {
		t.Fatal("watchdog helper did not use the private inherited-pipe launch contract")
	}
}

func TestWatchdogNormalCloseIsIdempotent(t *testing.T) {
	tree, err := Start(exec.Command("/bin/sh", "-c", "sleep 30"))
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tree.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestWatchdogHostSIGKILLReapsSetsidDescendantWithoutLeakingCredential(t *testing.T) {
	directory := t.TempDir()
	leaderFile := filepath.Join(directory, "leader.pid")
	helperFile := filepath.Join(directory, "helper.pid")
	childFile := filepath.Join(directory, "child.pid")
	secret := "watchdog-private-probe-7b1c09a2"
	host := exec.Command(os.Args[0], "-test.run=^TestProviderWatchdogHostFixture$")
	host.Env = append(os.Environ(),
		watchdogHostFixtureEnvironment+"=1",
		watchdogChildFixtureEnvironment+"=1",
		watchdogLeaderPIDEnvironment+"="+leaderFile,
		watchdogHelperPIDEnvironment+"="+helperFile,
		watchdogChildPIDEnvironment+"="+childFile,
		"TEAMVA_TEST_PROVIDER_CREDENTIAL="+secret,
	)
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	host.Stdout, host.Stderr = devNull, devNull
	if err := host.Start(); err != nil {
		_ = devNull.Close()
		t.Fatal(err)
	}
	_ = devNull.Close()
	defer func() {
		_ = host.Process.Kill()
		_ = host.Wait()
	}()
	leaderPID := waitForPublishedPID(t, leaderFile)
	helperPID := waitForPublishedPID(t, helperFile)
	childPID := waitForPublishedPID(t, childFile)
	if processImageContainsPrivateValue(helperPID, secret) {
		t.Fatal("Provider credential leaked into watchdog argv or environment")
	}
	if err := host.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = host.Wait()
	assertProcessGone(t, leaderPID)
	assertProcessGone(t, childPID)
	assertProcessGone(t, helperPID)
}

func TestProviderWatchdogHostFixture(t *testing.T) {
	if os.Getenv(watchdogHostFixtureEnvironment) != "1" {
		return
	}
	script := `printf '%s' "$$" > "$1"; printf '%s' "$PPID" > "$2"; "$3" -test.run=^TestProviderWatchdogSetsidChildFixture$ & wait`
	command := exec.Command("/bin/sh", "-c", script, "provider",
		os.Getenv(watchdogLeaderPIDEnvironment), os.Getenv(watchdogHelperPIDEnvironment), os.Args[0])
	command.Env = os.Environ()
	tree, err := Start(command)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	select {}
}

func TestProviderWatchdogSetsidChildFixture(t *testing.T) {
	if os.Getenv(watchdogChildFixtureEnvironment) != "1" {
		return
	}
	if _, err := syscall.Setsid(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv(watchdogChildPIDEnvironment),
		[]byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	select {}
}

func waitForPublishedPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		payload, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(payload)))
			if parseErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process PID was not published at %s", path)
	return 0
}

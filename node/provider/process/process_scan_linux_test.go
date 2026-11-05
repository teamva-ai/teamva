//go:build linux

package providerprocess

import (
	"os"
	"os/exec"
	"testing"
)

func TestLeaseScannerFindsProviderEnvironment(t *testing.T) {
	lease := "scanner-probe-lease"
	command := exec.Command("/bin/sleep", "5")
	command.Env = []string{watchdogLeaseEnvironment + "=" + lease}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	}()
	records, err := snapshotLeaseProcesses(uint32(os.Getuid()), lease)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Identity.PID == command.Process.Pid && record.HasLease {
			return
		}
	}
	t.Fatalf("lease scanner did not recognize Provider PID %d", command.Process.Pid)
}

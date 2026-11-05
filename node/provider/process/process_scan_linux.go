//go:build linux

package providerprocess

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const maxLinuxProcessMetadataBytes = 64 << 10

func snapshotLeaseProcesses(uid uint32, lease string) ([]watchedProcess, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	// Keep the current same-UID snapshot complete; only owned live identities
	// count toward maxWatchedProcesses in the tracker.
	records := make([]watchedProcess, 0, min(len(entries), maxWatchedProcesses))
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		record, processUID, err := linuxProcessRecord(pid)
		if err != nil || processUID != uid {
			continue
		}
		record.HasLease = linuxProcessHasFirstEnvironmentEntry(pid,
			watchdogLeaseEnvironment+"="+lease)
		records = append(records, record)
	}
	return records, nil
}

func linuxProcessRecord(pid int) (watchedProcess, uint32, error) {
	path := "/proc/" + strconv.Itoa(pid)
	var status unix.Stat_t
	if err := unix.Stat(path, &status); err != nil {
		return watchedProcess{}, 0, err
	}
	payload, err := readLinuxProcessFile(path+"/stat", maxLinuxProcessMetadataBytes)
	if err != nil {
		return watchedProcess{}, 0, err
	}
	closeParenthesis := bytes.LastIndexByte(payload, ')')
	if closeParenthesis < 0 || closeParenthesis+2 >= len(payload) {
		return watchedProcess{}, 0, errors.New("invalid Linux process stat")
	}
	fields := strings.Fields(string(payload[closeParenthesis+2:]))
	if len(fields) <= 19 {
		return watchedProcess{}, 0, errors.New("short Linux process stat")
	}
	parentID, err := strconv.Atoi(fields[1])
	if err != nil {
		return watchedProcess{}, 0, err
	}
	birthTime, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || birthTime == 0 {
		return watchedProcess{}, 0, errors.New("invalid Linux process birth identity")
	}
	return watchedProcess{
		Identity: processIdentity{PID: pid, BirthTime: birthTime}, ParentID: parentID,
	}, status.Uid, nil
}

func lookupProcessRecord(pid int) (watchedProcess, uint32, error) {
	return linuxProcessRecord(pid)
}

func linuxProcessHasFirstEnvironmentEntry(pid int, expected string) bool {
	file, err := os.Open("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return false
	}
	defer file.Close()
	// The watchdog prepends the lease, so no credential-bearing environment has
	// to be read merely to discover ownership.
	payload, err := io.ReadAll(io.LimitReader(file, int64(len(expected)+1)))
	if err != nil {
		return false
	}
	return len(payload) == len(expected)+1 && payload[len(payload)-1] == 0 &&
		string(payload[:len(payload)-1]) == expected
}

func readLinuxProcessFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > limit {
		return nil, errors.New("Linux process metadata is too large")
	}
	return payload, nil
}

func killProcessIdentity(record watchedProcess, uid uint32) error {
	current, processUID, err := linuxProcessRecord(record.Identity.PID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	if processUID != uid || current.Identity != record.Identity {
		return nil
	}
	fd, err := unix.PidfdOpen(record.Identity.PID, 0)
	if err == nil {
		defer unix.Close(fd)
		current, processUID, checkErr := linuxProcessRecord(record.Identity.PID)
		if checkErr != nil || processUID != uid || current.Identity != record.Identity {
			return nil
		}
		if err := unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
			return err
		}
		return nil
	}
	if !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.EPERM) {
		return err
	}
	current, processUID, err = linuxProcessRecord(record.Identity.PID)
	if err != nil || processUID != uid || current.Identity != record.Identity {
		return nil
	}
	err = unix.Kill(record.Identity.PID, unix.SIGKILL)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}

func processImageContainsPrivateValue(pid int, value string) bool {
	for _, name := range []string{"cmdline", "environ"} {
		payload, err := readLinuxProcessFile("/proc/"+strconv.Itoa(pid)+"/"+name,
			maxWatchdogSpecificationBytes)
		if err == nil && bytes.Contains(payload, []byte(value)) {
			return true
		}
	}
	return false
}

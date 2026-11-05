//go:build darwin

package providerprocess

import (
	"bytes"
	"encoding/binary"
	"errors"
	"runtime"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	maxDarwinLeaseInspections = 16
	maxDarwinProcArgsBytes    = 64 << 10
)

var darwinLeaseScanCursor atomic.Uint64

func snapshotLeaseProcesses(uid uint32, lease string) ([]watchedProcess, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	// The ownership limit applies to tracked live identities, not the first
	// same-UID processes in the OS list. A truncated snapshot could evict a
	// still-live tracked descendant and hide new children after process churn.
	records := make([]watchedProcess, 0, min(len(processes), maxWatchedProcesses))
	for _, process := range processes {
		record, processUID, ok := darwinProcessRecord(process)
		if !ok || processUID != uid {
			continue
		}
		records = append(records, record)
	}
	// kern.procargs2 requires a caller-provided buffer. Rotate a small bounded
	// subset on each poll instead of allocating ARG_MAX for every same-UID
	// process. Parent ancestry is still inspected for every process each poll.
	if len(records) > 0 {
		inspections := min(len(records), maxDarwinLeaseInspections)
		cursor := darwinLeaseScanCursor.Add(uint64(inspections)) - uint64(inspections)
		start := int(cursor % uint64(len(records)))
		for index := 0; index < inspections; index++ {
			recordIndex := (start + index) % len(records)
			records[recordIndex].HasLease = darwinProcessHasFirstEnvironmentEntry(
				records[recordIndex].Identity.PID, watchdogLeaseEnvironment+"="+lease)
		}
	}
	return records, nil
}

func darwinProcessRecord(process unix.KinfoProc) (watchedProcess, uint32, bool) {
	pid := int(process.Proc.P_pid)
	seconds := process.Proc.P_starttime.Sec
	microseconds := process.Proc.P_starttime.Usec
	if pid <= 0 || seconds <= 0 || microseconds < 0 {
		return watchedProcess{}, 0, false
	}
	birthTime := uint64(seconds)*1_000_000 + uint64(microseconds)
	return watchedProcess{
		Identity: processIdentity{PID: pid, BirthTime: birthTime},
		ParentID: int(process.Eproc.Ppid),
	}, process.Eproc.Ucred.Uid, true
}

func lookupProcessRecord(pid int) (watchedProcess, uint32, error) {
	process, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return watchedProcess{}, 0, err
	}
	record, uid, ok := darwinProcessRecord(*process)
	if !ok {
		return watchedProcess{}, 0, errors.New("invalid Darwin process record")
	}
	return record, uid, nil
}

func darwinProcessHasFirstEnvironmentEntry(pid int, expected string) bool {
	payload, err := darwinProcessArguments(pid)
	if err != nil {
		return false
	}
	entry := darwinFirstEnvironmentEntry(payload)
	return string(entry) == expected
}

func darwinProcessArguments(pid int) ([]byte, error) {
	argumentLimit, err := unix.SysctlUint32("kern.argmax")
	if err != nil {
		return nil, err
	}
	if argumentLimit == 0 {
		return nil, errors.New("invalid Darwin process argument limit")
	}
	bufferSize := min(int(argumentLimit), maxDarwinProcArgsBytes)
	payload := make([]byte, bufferSize)
	mib := [...]int32{unix.CTL_KERN, 49 /* KERN_PROCARGS2 */, int32(pid)}
	size := uintptr(len(payload))
	_, _, callErr := unix.Syscall6(unix.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)),
		uintptr(unsafe.Pointer(&payload[0])), uintptr(unsafe.Pointer(&size)), 0, 0)
	runtime.KeepAlive(mib)
	runtime.KeepAlive(payload)
	if callErr != 0 {
		return nil, callErr
	}
	if size > uintptr(len(payload)) {
		return nil, errors.New("Darwin process arguments exceeded their bound")
	}
	return payload[:size], nil
}

func darwinFirstEnvironmentEntry(payload []byte) []byte {
	if len(payload) < 4 {
		return nil
	}
	argumentCount := int(binary.LittleEndian.Uint32(payload[:4]))
	if argumentCount < 0 || argumentCount > 1<<20 {
		return nil
	}
	position := 4
	position = skipDarwinCString(payload, position)
	position = skipDarwinNUL(payload, position)
	for index := 0; index < argumentCount && position < len(payload); index++ {
		position = skipDarwinCString(payload, position)
	}
	position = skipDarwinNUL(payload, position)
	if position >= len(payload) {
		return nil
	}
	end := bytes.IndexByte(payload[position:], 0)
	if end < 0 {
		return nil
	}
	return payload[position : position+end]
}

func skipDarwinCString(payload []byte, position int) int {
	if position >= len(payload) {
		return len(payload)
	}
	end := bytes.IndexByte(payload[position:], 0)
	if end < 0 {
		return len(payload)
	}
	return position + end + 1
}

func skipDarwinNUL(payload []byte, position int) int {
	for position < len(payload) && payload[position] == 0 {
		position++
	}
	return position
}

func killProcessIdentity(record watchedProcess, uid uint32) error {
	process, err := unix.SysctlKinfoProc("kern.proc.pid", record.Identity.PID)
	if err != nil {
		if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	current, processUID, ok := darwinProcessRecord(*process)
	if !ok || processUID != uid || current.Identity != record.Identity {
		return nil
	}
	err = unix.Kill(record.Identity.PID, unix.SIGKILL)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}

func processImageContainsPrivateValue(pid int, value string) bool {
	payload, err := darwinProcessArguments(pid)
	return err == nil && bytes.Contains(payload, []byte(value))
}

//go:build darwin || linux

package providerprocess

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const (
	watchdogModeEnvironment       = "TEAMVA_INTERNAL_PROVIDER_WATCHDOG"
	watchdogLeaseEnvironment      = "TEAMVA_INTERNAL_PROVIDER_LEASE"
	maxWatchdogSpecificationBytes = 4 << 20
	maxWatchdogStatusBytes        = 16 << 10
	maxWatchedProcesses           = 4096
	watchdogPollInterval          = 20 * time.Millisecond
	watchdogCleanupTimeout        = 8 * time.Second
	watchdogSpecificationFD       = 3
	watchdogControlFD             = 4
	watchdogStatusFD              = 5
	watchdogFirstProviderFD       = 6
)

type watchdogSpec struct {
	Path       string   `json:"path"`
	Args       []string `json:"args"`
	Env        []string `json:"env"`
	Dir        string   `json:"dir,omitempty"`
	ExtraFiles int      `json:"extra_files,omitempty"`
}

type watchdogStatus struct {
	ProviderPID int    `json:"provider_pid,omitempty"`
	Error       string `json:"error,omitempty"`
}

type processIdentity struct {
	PID       int
	BirthTime uint64
}

type watchedProcess struct {
	Identity processIdentity
	ParentID int
	HasLease bool
}

// The private helper must take over before normal CLI/test argument parsing.
// Its environment contains only the fixed mode marker; Provider arguments,
// environment, working directory, and random lease travel through inherited
// pipes and are never logged or persisted.
func init() {
	if os.Getenv(watchdogModeEnvironment) != "1" {
		return
	}
	specification := os.NewFile(watchdogSpecificationFD, "provider-watchdog-specification")
	control := os.NewFile(watchdogControlFD, "provider-watchdog-control")
	status := os.NewFile(watchdogStatusFD, "provider-watchdog-status")
	code := runWatchdog(specification, control, status)
	_ = specification.Close()
	_ = control.Close()
	_ = status.Close()
	os.Exit(code)
}

func runWatchdog(specificationReader, controlReader, statusWriter *os.File) int {
	specification, err := readWatchdogSpec(specificationReader)
	if err != nil {
		writeWatchdogStatus(statusWriter, watchdogStatus{Error: "invalid Provider watchdog specification"})
		return 127
	}
	_ = specificationReader.Close()
	lease, err := newWatchdogLease()
	if err != nil {
		writeWatchdogStatus(statusWriter, watchdogStatus{Error: "create Provider watchdog lease"})
		return 127
	}
	command := exec.Command(specification.Path)
	command.Args = append([]string(nil), specification.Args...)
	command.Env = withWatchdogLease(specification.Env, lease)
	command.Dir = specification.Dir
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	for index := 0; index < specification.ExtraFiles; index++ {
		command.ExtraFiles = append(command.ExtraFiles,
			os.NewFile(uintptr(watchdogFirstProviderFD+index), fmt.Sprintf("provider-extra-%d", index)))
	}
	if err := command.Start(); err != nil {
		closeExtraFiles(command.ExtraFiles)
		writeWatchdogStatus(statusWriter, watchdogStatus{Error: "start Provider process: " + err.Error()})
		return 127
	}
	closeExtraFiles(command.ExtraFiles)
	tracker := newProcessTracker(uint32(os.Getuid()), lease)
	tracker.seed(command.Process.Pid)
	tracker.observe()
	writeWatchdogStatus(statusWriter, watchdogStatus{ProviderPID: command.Process.Pid})
	_ = statusWriter.Close()

	providerDone := make(chan error, 1)
	go func() { providerDone <- command.Wait() }()
	parentDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, controlReader)
		close(parentDone)
	}()
	ticker := time.NewTicker(watchdogPollInterval)
	defer ticker.Stop()
	for {
		select {
		case providerErr := <-providerDone:
			if !tracker.cleanup(nil, command.Process.Pid) {
				return 125
			}
			return processExitCode(providerErr)
		case <-parentDone:
			if !tracker.cleanup(command.Process, command.Process.Pid) {
				return 125
			}
			select {
			case <-providerDone:
			case <-time.After(time.Second):
				return 125
			}
			return 0
		case <-ticker.C:
			tracker.observe()
		}
	}
}

func readWatchdogSpec(reader io.Reader) (watchdogSpec, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, maxWatchdogSpecificationBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maxWatchdogSpecificationBytes {
		return watchdogSpec{}, errors.New("invalid watchdog specification size")
	}
	var specification watchdogSpec
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&specification); err != nil {
		return watchdogSpec{}, err
	}
	if specification.Path == "" || len(specification.Args) == 0 || specification.ExtraFiles < 0 ||
		specification.ExtraFiles > 64 {
		return watchdogSpec{}, errors.New("invalid watchdog specification")
	}
	return specification, nil
}

func writeWatchdogStatus(writer io.Writer, status watchdogStatus) {
	// Ignore transport errors: the host may already have died, in which case the
	// liveness pipe drives cleanup. Never print status or private launch state.
	_ = json.NewEncoder(writer).Encode(status)
}

func newWatchdogLease() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func withWatchdogLease(environment []string, lease string) []string {
	prefix := watchdogLeaseEnvironment + "="
	result := make([]string, 0, len(environment)+1)
	result = append(result, prefix+lease)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return result
}

func closeExtraFiles(files []*os.File) {
	for _, file := range files {
		if file != nil {
			_ = file.Close()
		}
	}
}

type processTracker struct {
	uid     uint32
	lease   string
	known   map[processIdentity]struct{}
	records map[processIdentity]watchedProcess
}

func newProcessTracker(uid uint32, lease string) *processTracker {
	return &processTracker{
		uid: uid, lease: lease,
		known: make(map[processIdentity]struct{}), records: make(map[processIdentity]watchedProcess),
	}
}

func (tracker *processTracker) seed(pid int) {
	record, processUID, err := lookupProcessRecord(pid)
	if err == nil && processUID == tracker.uid {
		tracker.known[record.Identity] = struct{}{}
		tracker.records[record.Identity] = record
	}
}

func (tracker *processTracker) observe() int {
	records, err := snapshotLeaseProcesses(tracker.uid, tracker.lease)
	if err != nil {
		return -1
	}
	return tracker.observeSnapshot(records, maxWatchedProcesses)
}

// observeSnapshot keeps only currently observed birth identities. Retaining
// historical PIDs both exhausts the limit after ordinary child churn and risks
// treating a reused PID as a still-owned ancestor. The snapshot already filters
// by UID; signalling independently rechecks UID and birth identity.
func (tracker *processTracker) observeSnapshot(records []watchedProcess, limit int) int {
	current := make(map[processIdentity]watchedProcess, len(records))
	for _, record := range records {
		current[record.Identity] = record
	}
	for identity := range tracker.known {
		if _, exists := current[identity]; !exists {
			delete(tracker.known, identity)
		}
	}
	tracker.records = make(map[processIdentity]watchedProcess, min(len(records), limit))
	knownPIDs := make(map[int]processIdentity, len(tracker.known))
	for _, record := range records {
		if _, exists := tracker.known[record.Identity]; exists {
			tracker.records[record.Identity] = record
			knownPIDs[record.Identity.PID] = record.Identity
		}
	}
	for {
		changed := false
		for _, record := range records {
			if len(tracker.known) >= limit {
				break
			}
			_, known := tracker.known[record.Identity]
			parent, knownParent := knownPIDs[record.ParentID]
			// A mixed-time snapshot must not attach a child to a different
			// process that reused its parent's PID after the child was born.
			knownParent = knownParent && record.Identity.BirthTime >= parent.BirthTime
			if !known && (record.HasLease || knownParent) {
				tracker.known[record.Identity] = struct{}{}
				tracker.records[record.Identity] = record
				knownPIDs[record.Identity.PID] = record.Identity
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return len(tracker.known)
}

func (tracker *processTracker) cleanup(provider *os.Process, providerGroup int) bool {
	// Capture ancestry before killing the leader: a just-forked child may not
	// have appeared in the periodic scan yet and will be reparented on exit.
	tracker.observe()
	if provider != nil {
		_ = provider.Kill()
	}
	// The Provider creates this group before it can fork. While any ordinary
	// descendant remains, Unix cannot reuse the group ID; signal it once before
	// relying on birth-identity-checked per-process cleanup for setsid children.
	if providerGroup > 0 {
		_ = syscall.Kill(-providerGroup, syscall.SIGKILL)
	}
	deadline := time.Now().Add(watchdogCleanupTimeout)
	quietScans := 0
	for time.Now().Before(deadline) {
		alive := tracker.observe()
		if alive == 0 {
			quietScans++
			if quietScans >= 3 {
				return true
			}
		} else {
			quietScans = 0
			for identity, record := range tracker.records {
				if _, tracked := tracker.known[identity]; tracked && identity.PID != os.Getpid() {
					_ = killProcessIdentity(record, tracker.uid)
				}
			}
		}
		time.Sleep(watchdogPollInterval)
	}
	return tracker.observe() == 0
}

func processExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		if status, ok := exitError.Sys().(syscall.WaitStatus); ok {
			if status.Signaled() {
				return 128 + int(status.Signal())
			}
			if code := status.ExitStatus(); code >= 0 && code <= 255 {
				return code
			}
		}
	}
	return 1
}

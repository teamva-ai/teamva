//go:build windows

package providerprocess

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func configureCommand(command *exec.Cmd) (func() error, func() error, func(), error) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create Provider job object: %w", err)
	}
	information := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	information.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&information)), uint32(unsafe.Sizeof(information))); err != nil {
		windows.CloseHandle(job)
		return nil, nil, nil, fmt.Errorf("configure Provider job object: %w", err)
	}
	var closeOnce sync.Once
	release := func() { closeOnce.Do(func() { _ = windows.CloseHandle(job) }) }
	afterStart := func() error {
		if command.Process == nil {
			return errors.New("provider process did not start")
		}
		process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|
			windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(command.Process.Pid))
		if err != nil {
			return fmt.Errorf("open suspended Provider process: %w", err)
		}
		defer windows.CloseHandle(process)
		if err := windows.AssignProcessToJobObject(job, process); err != nil {
			return fmt.Errorf("assign Provider process to job: %w", err)
		}
		if err := resumeProcessThreads(uint32(command.Process.Pid)); err != nil {
			return fmt.Errorf("resume Provider process: %w", err)
		}
		return nil
	}
	terminate := func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := windows.TerminateJobObject(job, 1)
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return nil
		}
		return err
	}
	return afterStart, terminate, release, nil
}

func resumeProcessThreads(processID uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		return err
	}
	resumed := false
	for {
		if entry.OwnerProcessID == processID {
			thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME|windows.THREAD_QUERY_INFORMATION,
				false, entry.ThreadID)
			if openErr != nil {
				return openErr
			}
			_, resumeErr := windows.ResumeThread(thread)
			windows.CloseHandle(thread)
			if resumeErr != nil {
				return resumeErr
			}
			resumed = true
		}
		entry.Size = uint32(unsafe.Sizeof(windows.ThreadEntry32{}))
		if err := windows.Thread32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				break
			}
			return err
		}
	}
	if !resumed {
		return errors.New("suspended Provider thread was not found")
	}
	return nil
}

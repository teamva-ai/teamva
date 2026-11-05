package gitread

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

func configureGitProcess(command *exec.Cmd) (func() error, func(), error) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED}
	job, jobErr := windows.CreateJobObject(nil, nil)
	if jobErr != nil {
		return nil, func() {}, fmt.Errorf("create Git job object: %w", jobErr)
	}
	information := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	information.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&information)), uint32(unsafe.Sizeof(information))); err != nil {
		windows.CloseHandle(job)
		return nil, func() {}, fmt.Errorf("configure Git job object: %w", err)
	}
	var closeOnce sync.Once
	closeJob := func() {
		closeOnce.Do(func() {
			_ = windows.CloseHandle(job)
		})
	}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		_ = windows.TerminateJobObject(job, 1)
		return command.Process.Kill()
	}
	afterStart := func() error {
		if command.Process == nil {
			return errors.New("Git process did not start")
		}
		process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|
			windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(command.Process.Pid))
		if err != nil {
			return fmt.Errorf("open suspended Git process: %w", err)
		}
		defer windows.CloseHandle(process)
		if err := windows.AssignProcessToJobObject(job, process); err != nil {
			return fmt.Errorf("assign suspended Git process to job: %w", err)
		}
		if err := resumeProcessThreads(uint32(command.Process.Pid)); err != nil {
			return fmt.Errorf("resume job-bound Git process: %w", err)
		}
		return nil
	}
	return afterStart, closeJob, nil
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
		return errors.New("suspended Git thread was not found")
	}
	return nil
}

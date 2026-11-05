//go:build darwin || linux

package providerprocess

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

func configureCommand(command *exec.Cmd) (func() error, func() error, func(), error) {
	if command.Process != nil {
		return nil, nil, nil, errors.New("Provider command was already started")
	}
	if command.Path == "" || len(command.Args) == 0 {
		return nil, nil, nil, errors.New("Provider executable is required")
	}
	if command.SysProcAttr != nil {
		return nil, nil, nil, errors.New("custom Provider SysProcAttr is unsupported with the Unix watchdog")
	}
	specification := watchdogSpec{
		Path: command.Path, Args: append([]string(nil), command.Args...),
		Env: command.Environ(), Dir: command.Dir, ExtraFiles: len(command.ExtraFiles),
	}
	specificationBytes, err := json.Marshal(specification)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("encode Provider watchdog specification: %w", err)
	}
	if len(specificationBytes) > maxWatchdogSpecificationBytes {
		return nil, nil, nil, fmt.Errorf("Provider watchdog specification exceeds %d bytes", maxWatchdogSpecificationBytes)
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolve Provider watchdog executable: %w", err)
	}
	specificationReader, specificationWriter, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, err
	}
	controlReader, controlWriter, err := os.Pipe()
	if err != nil {
		_ = specificationReader.Close()
		_ = specificationWriter.Close()
		return nil, nil, nil, err
	}
	statusReader, statusWriter, err := os.Pipe()
	if err != nil {
		_ = specificationReader.Close()
		_ = specificationWriter.Close()
		_ = controlReader.Close()
		_ = controlWriter.Close()
		return nil, nil, nil, err
	}
	originalExtraFiles := append([]*os.File(nil), command.ExtraFiles...)
	helper := exec.Command(executable)
	// The helper environment intentionally contains no Provider environment or
	// lease. All private launch state is delivered over specificationReader.
	helper.Env = []string{watchdogModeEnvironment + "=1"}
	helper.Stdin, helper.Stdout, helper.Stderr = command.Stdin, command.Stdout, command.Stderr
	helper.ExtraFiles = append([]*os.File{specificationReader, controlReader, statusWriter}, originalExtraFiles...)
	helper.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	*command = *helper

	var closeOnce sync.Once
	release := func() {
		closeOnce.Do(func() {
			_ = specificationReader.Close()
			_ = specificationWriter.Close()
			_ = controlReader.Close()
			_ = controlWriter.Close()
			_ = statusReader.Close()
			_ = statusWriter.Close()
		})
	}
	afterStart := func() error {
		if command.Process == nil {
			return errors.New("provider process did not start")
		}
		// Close the parent's duplicate child endpoints before waiting for the
		// watchdog. In particular, retaining controlReader would hide parent death.
		_ = specificationReader.Close()
		_ = controlReader.Close()
		_ = statusWriter.Close()
		if _, err := io.Copy(specificationWriter, bytes.NewReader(specificationBytes)); err != nil {
			_ = specificationWriter.Close()
			return fmt.Errorf("send Provider watchdog specification: %w", err)
		}
		_ = specificationWriter.Close()
		_ = statusReader.SetReadDeadline(time.Now().Add(10 * time.Second))
		limited := io.LimitReader(statusReader, maxWatchdogStatusBytes+1)
		var status watchdogStatus
		decoder := json.NewDecoder(limited)
		if err := decoder.Decode(&status); err != nil {
			_ = statusReader.Close()
			return fmt.Errorf("read Provider watchdog status: %w", err)
		}
		_ = statusReader.Close()
		if status.Error != "" {
			return errors.New(status.Error)
		}
		if status.ProviderPID <= 0 {
			return errors.New("Provider watchdog returned an invalid process ID")
		}
		return nil
	}
	terminate := func() error {
		err := controlWriter.Close()
		if errors.Is(err, os.ErrClosed) {
			return nil
		}
		return err
	}
	return afterStart, terminate, release, nil
}

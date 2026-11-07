//go:build windows

package client

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Start suspended so no child can escape before job assignment. Each execution
// owns a job; cancellation (or closing the job after Wait) stops its descendants
// without touching another session. Setup failures never leave a suspended child.
func startExecCommand(cmd *exec.Cmd) (func(), error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create command job: %w", err)
	}
	release := func() { windows.CloseHandle(job) }
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		release()
		return nil, fmt.Errorf("configure command job: %w", err)
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// cmd.exe parses a raw command tail, not CommandLineToArgvW arguments.
	// Go's default quoting adds backslashes to embedded quotes and breaks
	// commands such as a quoted executable path. Preserve the shell syntax.
	if strings.EqualFold(filepath.Base(cmd.Path), "cmd.exe") && len(cmd.Args) == 3 && cmd.Args[1] == "/c" && cmd.SysProcAttr.CmdLine == "" {
		cmd.SysProcAttr.CmdLine = syscall.EscapeArg(cmd.Args[0]) + " /c " + cmd.Args[2]
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	// CommandContext can call Cancel immediately after Start. Serialize it with
	// assignment/resumption so it cannot terminate an empty job and miss the shell.
	var setup sync.Mutex
	cmd.Cancel = func() error {
		setup.Lock()
		defer setup.Unlock()
		return windows.TerminateJobObject(job, 1)
	}
	setup.Lock()
	if err = cmd.Start(); err == nil {
		err = assignAndResumeCommand(job, uint32(cmd.Process.Pid))
	}
	setup.Unlock()
	if err != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		release()
		return nil, err
	}
	return release, nil
}

func assignAndResumeCommand(job windows.Handle, pid uint32) error {
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return fmt.Errorf("open command process: %w", err)
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		return fmt.Errorf("assign command job: %w", err)
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("find command thread: %w", err)
	}
	defer windows.CloseHandle(snapshot)
	thread := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &thread); err == nil; err = windows.Thread32Next(snapshot, &thread) {
		if thread.OwnerProcessID != pid {
			continue
		}
		handle, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, thread.ThreadID)
		if err != nil {
			return fmt.Errorf("open command thread: %w", err)
		}
		_, err = windows.ResumeThread(handle)
		windows.CloseHandle(handle)
		if err != nil {
			return fmt.Errorf("resume command thread: %w", err)
		}
		return nil
	}
	return fmt.Errorf("find suspended command thread: %w", err)
}

//go:build windows

package scanbroker

import (
	"fmt"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobAccounting mirrors JOBOBJECT_BASIC_ACCOUNTING_INFORMATION (the fields
// the benchmark needs); x/sys/windows declares the info class but not the
// struct.
type jobAccounting struct {
	totalUserTime             int64
	totalKernelTime           int64
	thisPeriodTotalUserTime   int64
	thisPeriodTotalKernelTime int64
	totalPageFaultCount       uint32
	processesSwappedOut       uint32
	processesSwappedIn        uint32
	processTerminations       uint32
}

// jobChildCPUSeconds sums user and kernel time of every process that ran in
// the job, in seconds (LARGE_INTEGER 100-nanosecond units).
func jobChildCPUSeconds(job windows.Handle) float64 {
	var info jobAccounting
	if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil {
		return 0
	}
	const hundredNS = float64(10000000)
	return float64(info.totalUserTime+info.totalKernelTime) / hundredNS
}

// assignJobObject places the subprocess in a kill-on-close Job Object owned
// by this process. Whatever happens to us — graceful exit, crash or a hard
// kill — the job handle closes and Windows terminates the scanner with it.
// The returned query reports the accumulated child CPU before the handle
// closes; benchmarks record it.
func assignJobObject(cmd *exec.Cmd) (stop func() error, queryChildCPU func() float64, err error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("create job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, nil, fmt.Errorf("configure job object: %w", err)
	}
	var assignErr error
	if err := cmd.Process.WithHandle(func(handle uintptr) {
		assignErr = windows.AssignProcessToJobObject(job, windows.Handle(handle))
	}); err != nil {
		_ = windows.CloseHandle(job)
		return nil, nil, fmt.Errorf("access scanner process handle: %w", err)
	}
	if assignErr != nil {
		_ = windows.CloseHandle(job)
		return nil, nil, fmt.Errorf("assign scanner to job object: %w", assignErr)
	}
	stop = func() error { return windows.CloseHandle(job) }
	queryChildCPU = func() float64 { return jobChildCPUSeconds(job) }
	return stop, queryChildCPU, nil
}

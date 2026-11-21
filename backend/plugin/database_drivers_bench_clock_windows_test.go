package plugin

import (
	"syscall"
	"time"
	"unsafe"
)

// benchNow is the benchmark clock on Windows. Go's monotonic clock there
// advances with the system timer tick (0.5–15.6 ms), which rounds sub-
// millisecond calls to 0 or one tick; QueryPerformanceCounter resolves
// well below a microsecond.
var (
	benchKernel32 = syscall.NewLazyDLL("kernel32.dll")
	benchQPC      = benchKernel32.NewProc("QueryPerformanceCounter")
	benchQPF      = benchKernel32.NewProc("QueryPerformanceFrequency")
	benchMemInfo  = benchKernel32.NewProc("K32GetProcessMemoryInfo")
	benchFreq     = func() int64 {
		var freq int64
		_, _, _ = benchQPF.Call(uintptr(unsafe.Pointer(&freq)))
		return freq
	}()
)

func benchNow() time.Duration {
	var counter int64
	_, _, _ = benchQPC.Call(uintptr(unsafe.Pointer(&counter)))
	if benchFreq <= 0 {
		return 0
	}
	return time.Duration(counter/benchFreq)*time.Second + time.Duration(counter%benchFreq)*time.Second/time.Duration(benchFreq)
}

// processRSSKiB returns a process's working set (the Windows counterpart of
// the resident set size).
func processRSSKiB(pid int) int {
	if pid <= 0 {
		return 0
	}
	const processQueryLimitedInformation = 0x1000
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return 0
	}
	defer syscall.CloseHandle(handle)
	// PROCESS_MEMORY_COUNTERS (64-bit layout).
	var counters struct {
		cb                         uint32
		pageFaultCount             uint32
		peakWorkingSetSize         uintptr
		workingSetSize             uintptr
		quotaPeakPagedPoolUsage    uintptr
		quotaPagedPoolUsage        uintptr
		quotaPeakNonPagedPoolUsage uintptr
		quotaNonPagedPoolUsage     uintptr
		pagefileUsage              uintptr
		peakPagefileUsage          uintptr
	}
	counters.cb = uint32(unsafe.Sizeof(counters))
	if ok, _, _ := benchMemInfo.Call(uintptr(handle), uintptr(unsafe.Pointer(&counters)), uintptr(counters.cb)); ok == 0 {
		return 0
	}
	return int(counters.workingSetSize / 1024)
}

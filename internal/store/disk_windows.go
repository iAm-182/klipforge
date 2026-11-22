//go:build windows

package store

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"klipforge/internal/model"
)

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procGetDiskFreeSpaceEx = kernel32.NewProc("GetDiskFreeSpaceExW")
)

// GetDiskStats inspects free/total disk space on the output volume plus KlipForge folder usage.
func GetDiskStats(outputDir, dataDir string, minFreeGB float64) model.DiskStats {
	if minFreeGB <= 0 {
		minFreeGB = 2.0
	}
	target := outputDir
	if target == "" {
		target = dataDir
	}
	absPath, err := filepath.Abs(target)
	if err != nil {
		absPath = target
	}
	_ = os.MkdirAll(absPath, 0o755)

	var freeBytesAvail, totalBytes, totalFree uint64
	if ptr, err := syscall.UTF16PtrFromString(absPath); err == nil {
		ret, _, _ := procGetDiskFreeSpaceEx.Call(
			uintptr(unsafe.Pointer(ptr)),
			uintptr(unsafe.Pointer(&freeBytesAvail)),
			uintptr(unsafe.Pointer(&totalBytes)),
			uintptr(unsafe.Pointer(&totalFree)),
		)
		if ret == 0 {
			freeBytesAvail = 0
			totalBytes = 0
		}
	}

	freeGB := float64(freeBytesAvail) / (1024 * 1024 * 1024)
	totalGB := float64(totalBytes) / (1024 * 1024 * 1024)

	return model.DiskStats{
		FreeBytes:     freeBytesAvail,
		TotalBytes:    totalBytes,
		FreeGB:        freeGB,
		TotalGB:       totalGB,
		OutputsBytes:  dirSizeBytes(outputDir),
		ProjectsBytes: dirSizeBytes(filepath.Join(dataDir, "projects")),
		MinFreeGB:     minFreeGB,
		LowDisk:       totalBytes > 0 && freeGB < minFreeGB,
	}
}

func dirSizeBytes(root string) int64 {
	if root == "" {
		return 0
	}
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			total += fi.Size()
		}
		return nil
	})
	return total
}

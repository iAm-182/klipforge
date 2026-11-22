//go:build !windows

package store

import (
	"os"
	"path/filepath"
	"syscall"

	"klipforge/internal/model"
)

// GetDiskStats inspects free/total disk space on Unix volumes plus KlipForge folder usage.
func GetDiskStats(outputDir, dataDir string, minFreeGB float64) model.DiskStats {
	if minFreeGB <= 0 {
		minFreeGB = 2.0
	}
	target := outputDir
	if target == "" {
		target = dataDir
	}
	_ = os.MkdirAll(target, 0o755)

	var st syscall.Statfs_t
	var freeBytes, totalBytes uint64
	if err := syscall.Statfs(target, &st); err == nil {
		freeBytes = st.Bavail * uint64(st.Bsize)
		totalBytes = st.Blocks * uint64(st.Bsize)
	}

	freeGB := float64(freeBytes) / (1024 * 1024 * 1024)
	totalGB := float64(totalBytes) / (1024 * 1024 * 1024)

	return model.DiskStats{
		FreeBytes:     freeBytes,
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

//go:build unix

package monitor

import (
	"path/filepath"
	"syscall"
)

// DiskUsage reports free/total bytes of the filesystem holding path.
func DiskUsage(path string) DiskInfo {
	if path == "" {
		path = "."
	}
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}
	info := DiskInfo{Path: path}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		info.Error = err.Error()
		return info
	}
	info.TotalBytes = st.Blocks * uint64(st.Bsize)
	info.FreeBytes = st.Bavail * uint64(st.Bsize)
	if info.TotalBytes > 0 {
		info.FreePercent = float64(info.FreeBytes) * 100 / float64(info.TotalBytes)
	}
	return info
}

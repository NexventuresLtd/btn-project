//go:build !unix

package monitor

// DiskUsage is not implemented on this platform.
func DiskUsage(path string) DiskInfo {
	return DiskInfo{Path: path, Error: "not supported on this platform"}
}

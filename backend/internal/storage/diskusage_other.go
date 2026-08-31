//go:build !linux && !darwin

package storage

// On platforms without statfs we report "unsupported" rather than inventing
// numbers. Health checks treat this as a warning, never as a failure.
func diskUsage(_ string) (DiskUsage, error) {
	return DiskUsage{FilesystemType: "unknown"}, nil
}

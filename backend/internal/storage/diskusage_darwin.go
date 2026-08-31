//go:build darwin

package storage

import "syscall"

// darwin support exists so the backend can be developed on a Mac; production
// targets are Linux on ARM64 and x86_64.
func diskUsage(path string) (DiskUsage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return DiskUsage{}, err
	}
	bsize := uint64(st.Bsize)
	total := st.Blocks * bsize
	free := st.Bavail * bsize
	name := make([]byte, 0, len(st.Fstypename))
	for _, c := range st.Fstypename {
		if c == 0 {
			break
		}
		name = append(name, byte(c))
	}
	return DiskUsage{
		TotalBytes:     int64(total),
		FreeBytes:      int64(free),
		UsedBytes:      int64(total - st.Bfree*bsize),
		FilesystemType: string(name),
		InodesTotal:    int64(st.Files),
		InodesFree:     int64(st.Ffree),
		BlockSizeBytes: int64(bsize),
		usageSupported: true,
	}, nil
}

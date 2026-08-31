//go:build linux

package storage

import "syscall"

func diskUsage(path string) (DiskUsage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return DiskUsage{}, err
	}
	bsize := uint64(st.Bsize)
	total := st.Blocks * bsize
	// Bavail is space available to an unprivileged user; Bfree includes the
	// root-reserved blocks. Reporting Bavail is what `df` shows and is the
	// number that actually matters for our non-root process.
	free := st.Bavail * bsize
	return DiskUsage{
		TotalBytes:     int64(total),
		FreeBytes:      int64(free),
		UsedBytes:      int64(total - st.Bfree*bsize),
		FilesystemType: filesystemName(int64(st.Type)),
		InodesTotal:    int64(st.Files),
		InodesFree:     int64(st.Ffree),
		BlockSizeBytes: int64(bsize),
		usageSupported: true,
	}, nil
}

// filesystemName maps the well-known Linux magic numbers to human labels so the
// admin UI can say "ext4" instead of "0xEF53".
func filesystemName(magic int64) string {
	switch magic {
	case 0xEF53:
		return "ext2/ext3/ext4"
	case 0x58465342:
		return "xfs"
	case 0x9123683E:
		return "btrfs"
	case 0x2FC12FC1:
		return "zfs"
	case 0x6969:
		return "nfs"
	case 0xFF534D42:
		return "cifs"
	case 0x4d44:
		return "vfat"
	case 0x5346544E:
		return "ntfs"
	case 0x01021994:
		return "tmpfs"
	case 0x794c7630:
		return "overlayfs"
	case 0x65735546:
		return "fuse"
	case 0x2011BAB0:
		return "exfat"
	case 0x52654973:
		return "reiserfs"
	case 0x0BD00BD0:
		return "lustre"
	case 0x73717368:
		return "squashfs"
	default:
		return "unknown"
	}
}

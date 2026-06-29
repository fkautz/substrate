// memsnap.go -- snapshot a runsc sentry's guest-memory memfd.
//
// Usage: memsnap <sentry-pid> <out-file>
//
// gVisor backs guest RAM in a memfd named "runsc-memory" (runsc/boot/loader.go),
// mapped offset-linearly (chunk i at memfd offset i*chunkSize, ledger F6). So the
// memfd's content IS the guest memory in guest-address order. This finds that fd
// under /proc/<pid>/fd and sparse-copies its committed extents (SEEK_DATA /
// SEEK_HOLE) to out, preserving offsets so two snapshots are page-comparable.
// Run as root: the sentry process is root-owned.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// SEEK_DATA / SEEK_HOLE (linux); not exported by the syscall package.
const seekData = 3
const seekHole = 4

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: memsnap <sentry-pid> <out-file>")
		os.Exit(2)
	}
	pid, out := os.Args[1], os.Args[2]

	fddir := "/proc/" + pid + "/fd"
	entries, err := os.ReadDir(fddir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "readdir:", err)
		os.Exit(1)
	}
	memfd := ""
	for _, e := range entries {
		link, _ := os.Readlink(filepath.Join(fddir, e.Name()))
		if strings.Contains(link, "runsc-memory") {
			memfd = filepath.Join(fddir, e.Name())
			break
		}
	}
	if memfd == "" {
		fmt.Fprintln(os.Stderr, "no runsc-memory fd found for pid", pid)
		os.Exit(1)
	}

	in, err := os.Open(memfd)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open memfd:", err)
		os.Exit(1)
	}
	defer in.Close()
	infd := int(in.Fd())
	size, err := in.Seek(0, io.SeekEnd)
	if err != nil {
		fmt.Fprintln(os.Stderr, "seek end:", err)
		os.Exit(1)
	}

	of, err := os.Create(out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "create out:", err)
		os.Exit(1)
	}
	defer of.Close()
	if err := of.Truncate(size); err != nil {
		fmt.Fprintln(os.Stderr, "truncate:", err)
		os.Exit(1)
	}
	outfd := int(of.Fd())

	buf := make([]byte, 1<<20)
	var committed int64
	for off := int64(0); off < size; {
		data, err := syscall.Seek(infd, off, seekData)
		if err != nil {
			break // no more data
		}
		hole, err := syscall.Seek(infd, data, seekHole)
		if err != nil {
			hole = size
		}
		for pos := data; pos < hole; {
			n := int64(len(buf))
			if hole-pos < n {
				n = hole - pos
			}
			rn, rerr := syscall.Pread(infd, buf[:n], pos)
			if rn > 0 {
				if _, werr := syscall.Pwrite(outfd, buf[:rn], pos); werr != nil {
					fmt.Fprintln(os.Stderr, "pwrite:", werr)
					os.Exit(1)
				}
				committed += int64(rn)
				pos += int64(rn)
			}
			if rerr != nil || rn == 0 {
				break
			}
		}
		off = hole
	}
	fmt.Printf("snap pid=%s fd=%s size=%dMiB committed=%dMiB out=%s\n",
		pid, filepath.Base(memfd), size>>20, committed>>20, out)
}

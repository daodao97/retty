package rex

import (
	"bytes"
	"encoding/binary"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

// procPidinfo is libproc's proc_pidinfo, loaded on first use.
var (
	procOnce    sync.Once
	procPidinfo func(pid, flavor int32, arg uint64, buf unsafe.Pointer, size int32) int32
)

const (
	procPidVnodePathInfo = 9
	// struct proc_vnodepathinfo: two vnode_info_path, each a vnode_info
	// (152 bytes) and a path of MAXPATHLEN bytes; the first is the
	// current directory.
	vnodeInfoSize = 152
	maxPathLen    = 1024
)

type processPathBuffer [2 * (vnodeInfoSize + maxPathLen)]byte

// Passing the buffer to libproc makes it escape. Reuse scratch storage rather
// than allocating it for every session in every poll; returned strings own
// their bytes, and every use still queries libproc for the current directory.
var processPathBuffers = sync.Pool{New: func() any { return new(processPathBuffer) }}

func loadLibproc() {
	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return
	}
	purego.RegisterLibFunc(&procPidinfo, lib, "proc_pidinfo")
}

// inspect returns the name, arguments and working directory of a process.
func inspect(pid int) (p procInfo) {
	if pid <= 0 {
		return
	}
	if k, err := unix.SysctlKinfoProc("kern.proc.pid", pid); err == nil {
		comm := k.Proc.P_comm[:]
		if i := bytes.IndexByte(comm, 0); i >= 0 {
			comm = comm[:i]
		}
		p.name = string(comm)
	}
	if raw, err := unix.SysctlRaw("kern.procargs2", pid); err == nil && len(raw) > 4 {
		argc := int(binary.LittleEndian.Uint32(raw))
		rest := raw[4:]
		// The executable's path, then NULs up to the arguments.
		if i := bytes.IndexByte(rest, 0); i >= 0 {
			rest = rest[i:]
		}
		for len(rest) > 0 && rest[0] == 0 {
			rest = rest[1:]
		}
		for len(p.args) < argc && len(rest) > 0 {
			i := bytes.IndexByte(rest, 0)
			if i < 0 {
				i = len(rest)
			}
			p.args = append(p.args, string(rest[:i]))
			rest = rest[min(i+1, len(rest)):]
		}
	}
	procOnce.Do(loadLibproc)
	if procPidinfo != nil {
		buf := processPathBuffers.Get().(*processPathBuffer)
		clear(buf[:])
		if procPidinfo(int32(pid), procPidVnodePathInfo, 0, unsafe.Pointer(&buf[0]), int32(len(buf))) > 0 {
			path := buf[vnodeInfoSize : vnodeInfoSize+maxPathLen]
			if i := bytes.IndexByte(path, 0); i >= 0 {
				path = path[:i]
			}
			p.dir = string(path)
		}
		processPathBuffers.Put(buf)
	}
	return p
}

func sysctlString(name string) string {
	s, _ := unix.Sysctl(name)
	return s
}

func sysctlUint64(name string) uint64 {
	v, _ := unix.SysctlUint64(name)
	return v
}

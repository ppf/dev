package desktop

import (
	"time"

	"golang.org/x/sys/unix"
)

// procStart is when process pid started, to the second.
func procStart(pid int) (time.Time, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(kp.Proc.P_pid) != pid {
		return time.Time{}, false
	}
	return time.Unix(kp.Proc.P_starttime.Sec, 0), true
}

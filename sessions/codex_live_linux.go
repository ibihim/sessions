package sessions

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func codexLiveProcs(home string) (map[string]liveProc, error) {
	return readCodexProcs("/proc", home, os.Getuid())
}

// A sandbox can expose the host's session files with only its own processes.
// In a remapped user namespace, missing host PIDs cannot establish an exit.
func processVisibility() error {
	data, err := os.ReadFile("/proc/self/uid_map")
	if err != nil {
		return err
	}
	return checkUIDMap(string(data))
}

func checkUIDMap(data string) error {
	fields := strings.Fields(data)
	if len(fields) != 3 || fields[0] != "0" || fields[1] != "0" || fields[2] != "4294967295" {
		return fmt.Errorf("process view is restricted by a user namespace; missing host processes have unknown live status")
	}
	return nil
}

// The lock descriptor, not the lock file, is the liveness authority. Inspect
// only our user's Codex processes; other users' inaccessible FDs are irrelevant.
func readCodexProcs(procRoot, home string, uid int) (map[string]liveProc, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}
	boot, hz, clockErr := procClock(procRoot)
	lockDir, err := filepath.Abs(filepath.Join(home, "thread-writer-locks"))
	if err != nil {
		return nil, err
	}
	if real, err := filepath.EvalSymlinks(lockDir); err == nil {
		lockDir = real
	}
	procs := make(map[string]liveProc)
	var problems []error
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		dir := filepath.Join(procRoot, entry.Name())
		info, err := os.Stat(dir)
		if err != nil {
			if !os.IsNotExist(err) {
				problems = append(problems, err)
			}
			continue
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != uid {
			continue
		}
		comm, err := os.ReadFile(filepath.Join(dir, "comm"))
		if err != nil {
			if !os.IsNotExist(err) {
				problems = append(problems, err)
			}
			continue
		}
		if strings.TrimSpace(string(comm)) != "codex" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, "stat"))
		if err != nil {
			if !os.IsNotExist(err) {
				problems = append(problems, err)
			}
			continue
		}
		ticks, tty, err := parseProcStat(string(data))
		if err != nil {
			problems = append(problems, fmt.Errorf("pid %d: %w", pid, err))
			continue
		}
		fds, err := os.ReadDir(filepath.Join(dir, "fd"))
		if err != nil {
			if !os.IsNotExist(err) {
				problems = append(problems, err)
			}
			continue
		}
		terminal := false
		if tty != 0 {
			for _, fd := range []string{"0", "1", "2"} {
				target, err := os.Readlink(filepath.Join(dir, "fd", fd))
				if err == nil && (strings.HasPrefix(target, "/dev/pts/") || strings.HasPrefix(target, "/dev/tty")) {
					terminal = true
				}
			}
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(dir, "fd", fd.Name()))
			if err != nil {
				if !os.IsNotExist(err) {
					problems = append(problems, err)
				}
				continue
			}
			if filepath.Dir(target) != lockDir || !strings.HasSuffix(target, ".lock") {
				continue
			}
			fdinfo, err := os.ReadFile(filepath.Join(dir, "fdinfo", fd.Name()))
			if err != nil {
				if !os.IsNotExist(err) {
					problems = append(problems, err)
				}
				continue
			}
			if !heldWriterLock(string(fdinfo), pid) {
				continue
			}
			id := strings.TrimSuffix(filepath.Base(target), ".lock")
			p := liveProc{PID: pid, SessionID: id, Entrypoint: "nonterminal"}
			if clockErr == nil {
				p.StartedAt = boot.Add(time.Duration(ticks/hz)*time.Second + time.Duration(ticks%hz)*time.Second/time.Duration(hz)).UnixMilli()
			}
			if terminal {
				p.Entrypoint = "cli"
			}
			procs[id] = p
		}
	}
	if clockErr != nil {
		problems = append(problems, clockErr)
	}
	return procs, errors.Join(problems...)
}

func heldWriterLock(info string, pid int) bool {
	for _, line := range strings.Split(info, "\n") {
		fields := strings.Fields(line)
		// lock: 1: FLOCK ADVISORY WRITE <pid> <device:inode> 0 EOF
		if len(fields) >= 9 && fields[0] == "lock:" && fields[2] == "FLOCK" &&
			fields[3] == "ADVISORY" && fields[4] == "WRITE" && fields[5] == strconv.Itoa(pid) &&
			fields[7] == "0" && fields[8] == "EOF" {
			return true
		}
	}
	return false
}

func parseProcStat(stat string) (ticks uint64, tty int64, err error) {
	// comm may itself contain spaces or ')'; fields after its last ')' are fixed.
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return 0, 0, fmt.Errorf("invalid process stat")
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 20 {
		return 0, 0, fmt.Errorf("short process stat")
	}
	ticks, err = strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	tty, err = strconv.ParseInt(fields[4], 10, 64)
	return ticks, tty, err
}

// Linux exposes CLK_TCK in the process auxiliary vector. Reading it avoids
// guessing the clock rate or starting getconf for every picker refresh.
func procClock(root string) (time.Time, uint64, error) {
	stat, err := os.ReadFile(filepath.Join(root, "stat"))
	if err != nil {
		return time.Time{}, 0, err
	}
	var boot int64
	for _, line := range strings.Split(string(stat), "\n") {
		if strings.HasPrefix(line, "btime ") {
			boot, err = strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "btime ")), 10, 64)
			if err != nil {
				return time.Time{}, 0, err
			}
		}
	}
	aux, err := os.ReadFile(filepath.Join(root, "self", "auxv"))
	if err != nil {
		return time.Time{}, 0, err
	}
	word := strconv.IntSize / 8
	read := func(b []byte) uint64 {
		if word == 8 {
			return binary.NativeEndian.Uint64(b)
		}
		return uint64(binary.NativeEndian.Uint32(b))
	}
	for i := 0; i+2*word <= len(aux); i += 2 * word {
		if read(aux[i:]) == 17 && boot != 0 { // AT_CLKTCK
			if hz := read(aux[i+word:]); hz > 0 {
				return time.Unix(boot, 0), hz, nil
			}
		}
	}
	return time.Time{}, 0, fmt.Errorf("process start clock unavailable")
}

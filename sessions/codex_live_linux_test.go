package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// Real kernel locks, with an isolated proc view pointing at this test process.
// Releasing a lock leaves BOTH the file and descriptor in place: neither alone
// may keep the session marked live. Reacquiring simulates the resume transition.
func TestCodexHeldReleasedAndReacquiredWriterLock(t *testing.T) {
	home, proc := t.TempDir(), t.TempDir()
	pid := os.Getpid()
	dir := filepath.Join(proc, fmt.Sprint(pid))
	putFile(t, filepath.Join(dir, "comm"), "codex\n")
	link := func(target, path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"stat", "fd", "fdinfo"} {
		link("/proc/self/"+name, filepath.Join(dir, name))
	}
	link("/proc/stat", filepath.Join(proc, "stat"))
	link("/proc/self/auxv", filepath.Join(proc, "self", "auxv"))
	lock := filepath.Join(home, "thread-writer-locks", "thread.lock")
	putFile(t, lock, "")
	f, err := os.OpenFile(lock, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	check := func(wantLive bool) liveProc {
		t.Helper()
		got, err := readCodexProcs(proc, home, os.Getuid())
		if err != nil {
			t.Fatal(err)
		}
		p, live := got["thread"]
		if live != wantLive {
			t.Fatalf("live=%v, want %v; procs=%+v", live, wantLive, got)
		}
		if live && (p.PID != pid || p.StartedAt == 0 || p.Status != "") {
			t.Fatalf("process evidence: %+v", p)
		}
		return p
	}
	check(false)
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	first := check(true)
	if again := check(true); again.StartedAt != first.StartedAt {
		t.Fatal("idle scan moved process start")
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	check(false)
	if _, err := os.Stat(lock); err != nil {
		t.Fatal("released lock file disappeared")
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	check(true)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	check(false)
}

func TestWriterLockEvidence(t *testing.T) {
	for _, tc := range []struct {
		info string
		want bool
	}{
		{"pos: 0\nlock: 1: FLOCK ADVISORY WRITE 42 00:1d:100 0 EOF\n", true},
		{"pos: 0\nflags: 02100002\n", false},
		{"lock: 1: FLOCK ADVISORY READ 42 00:1d:100 0 EOF", false},
		{"lock: 1: FLOCK ADVISORY WRITE 43 00:1d:100 0 EOF", false},
		{"lock: 1: -> FLOCK ADVISORY WRITE 42 00:1d:100 0 EOF", false},
		{"lock: 1: POSIX ADVISORY WRITE 42 00:1d:100 0 EOF", false},
	} {
		if got := heldWriterLock(tc.info, 42); got != tc.want {
			t.Errorf("heldWriterLock(%q)=%v", tc.info, got)
		}
	}
}

func TestProcStatAndUnavailableDetector(t *testing.T) {
	fields := strings.Fields("S 1 2 3 34819 5 6 7 8 9 10 11 12 13 14 15 16 17 18 987654")
	ticks, tty, err := parseProcStat("42 (name with ) parens) " + strings.Join(fields, " "))
	if err != nil || ticks != 987654 || tty != 34819 {
		t.Fatalf("stat: %d %d %v", ticks, tty, err)
	}
	if _, _, err := parseProcStat("broken"); err == nil {
		t.Fatal("accepted invalid stat")
	}
	if _, err := readCodexProcs(filepath.Join(t.TempDir(), "missing"), t.TempDir(), os.Getuid()); err == nil {
		t.Fatal("missing proc claimed a stopped session")
	}
}

func TestRestrictedProcessViewIsUnknown(t *testing.T) {
	if err := checkUIDMap("         0          0 4294967295\n"); err != nil {
		t.Fatal(err)
	}
	if err := checkUIDMap("      1000          0          1\n"); err == nil {
		t.Fatal("remapped namespace claimed complete host visibility")
	}
}

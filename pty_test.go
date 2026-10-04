//go:build darwin || linux

// A minimal pseudo-terminal harness, so the --stdout-safe refusal can be
// exercised the way a user hits it. runMain in main_test.go gives the child a
// pipe for stdout, which isInteractive() correctly reports as non-interactive,
// so it can never observe the refusal. These helpers allocate a real pty with
// only the standard library, put the child's stdout on the slave side, and
// drain the master side so a large dump cannot block the child on a full pty
// buffer. stderr stays an ordinary pipe, so the refusal message is captured
// verbatim instead of being mixed into the terminal's echo and CRLF stream.

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"unsafe"
)

// pty ioctl request numbers, spelled out because the syscall package exports
// the BSD ones (TIOCPTYGRANT and friends) but not the System V ones, and the
// two spellings cannot both be referenced from one file.
const (
	tiocptygrantDarwin = 0x20007454
	tiocptyunlkDarwin  = 0x20007452
	tiocptygnameDarwin = 0x40807453
	tiocsptlckLinux    = 0x40045431
	tiocgptnLinux      = 0x80045430
)

// openTestPTY returns the master and slave ends of a fresh pseudo-terminal, or
// ok == false after skipping the test if this machine cannot provide one.
func openTestPTY(t *testing.T) (master, slave *os.File, ok bool) {
	t.Helper()

	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("cannot open /dev/ptmx: %v", err)
	}

	var name string
	switch runtime.GOOS {
	case "darwin":
		// BSD-style multiplexer: grant and unlock the slave, then ask for
		// its name.
		if err := ptyIoctl(master, tiocptygrantDarwin); err != nil {
			master.Close()
			t.Skipf("TIOCPTYGRANT failed: %v", err)
		}
		if err := ptyIoctl(master, tiocptyunlkDarwin); err != nil {
			master.Close()
			t.Skipf("TIOCPTYUNLK failed: %v", err)
		}
		buf := make([]byte, 128)
		if err := ptyIoctlPtr(master, tiocptygnameDarwin, unsafe.Pointer(&buf[0])); err != nil {
			master.Close()
			t.Skipf("TIOCPTYGNAME failed: %v", err)
		}
		if i := bytes.IndexByte(buf, 0); i >= 0 {
			buf = buf[:i]
		}
		name = string(buf)

	case "linux":
		// System V-style multiplexer: unlock the slave, then ask for its
		// number and build the /dev/pts path from it.
		if err := ptyIoctlInt(master, tiocsptlckLinux, 0); err != nil {
			master.Close()
			t.Skipf("TIOCSPTLCK failed: %v", err)
		}
		n := uint32(0)
		if err := ptyIoctlPtr(master, tiocgptnLinux, unsafe.Pointer(&n)); err != nil {
			master.Close()
			t.Skipf("TIOCGPTN failed: %v", err)
		}
		name = "/dev/pts/" + strconv.Itoa(int(n))

	default:
		master.Close()
		t.Skipf("no pty harness for %s", runtime.GOOS)
	}

	slave, err = os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		master.Close()
		t.Skipf("cannot open pty slave %s: %v", name, err)
	}

	// Sanity check: the whole point is that the child's stdout looks like a
	// terminal. If it does not, the refusal under test is unreachable.
	fi, err := slave.Stat()
	if err != nil {
		master.Close()
		slave.Close()
		t.Skipf("cannot stat pty slave: %v", err)
	}
	if fi.Mode()&os.ModeCharDevice == 0 {
		master.Close()
		slave.Close()
		t.Skipf("pty slave is not a character device (%v), cannot test --stdout-safe", fi.Mode())
	}
	return master, slave, true
}

// ptyIoctl issues a tty ioctl that takes no argument.
func ptyIoctl(f *os.File, req uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

// ptyIoctlInt issues a tty ioctl whose argument is an int.
func ptyIoctlInt(f *os.File, req uintptr, arg int) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

// ptyIoctlPtr issues a tty ioctl whose argument is a pointer.
func ptyIoctlPtr(f *os.File, req uintptr, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

// runMainOnPTY re-executes the test binary with args, with stdout attached to a
// real pty, and returns mainResult with stdout holding the bytes the terminal
// received. Exit codes are reported rather than treated as failures, so one
// helper serves both the refusing and the dumping runs.
func runMainOnPTY(t *testing.T, args ...string) mainResult {
	t.Helper()

	master, slave, ok := openTestPTY(t)
	if !ok {
		t.Skip("no pty available")
	}

	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), everythingRunMainEnv+"=1")
	cmd.Stdin = slave
	cmd.Stdout = slave
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		slave.Close()
		master.Close()
		t.Fatalf("starting main(%v) on a pty: %v", args, err)
	}

	// The pty buffer is finite, so a run that dumps to the terminal would
	// block on write unless the master is drained while it runs.
	var mu sync.Mutex
	var terminal bytes.Buffer
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		buf := make([]byte, 64*1024)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				mu.Lock()
				terminal.Write(buf[:n])
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	code := 0
	if err := cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("waiting for main(%v): %v", args, err)
		}
		code = exitErr.ExitCode()
	}

	// The child is gone, so dropping this process's own slave reference is
	// what makes the master's pending read return EOF. Closing the master
	// instead does not: close(2) does not interrupt a read already blocked on
	// the other end of the same pty.
	slave.Close()
	<-drained
	master.Close()

	mu.Lock()
	defer mu.Unlock()
	return mainResult{stdout: terminal.String(), stderr: stderr.String(), code: code}
}

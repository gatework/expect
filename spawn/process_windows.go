//go:build windows

package spawn

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/gatework/expect"
)

// Process is a child running on a pseudo console, inside a job that kills
// it and its descendants when closed. It must not be copied.
type Process struct {
	pc      windows.Handle
	proc    windows.Handle
	job     windows.Handle
	pid     int
	s       *expect.Session
	stop    func() bool
	once    sync.Once
	status  ExitStatus
	waitErr error
	closeMu sync.Mutex // guards the handles during Close
	closed  bool
}

// Start runs program with args on a new pseudo console (Windows 10 1809
// or later). Canceling ctx terminates the job; it does not close the
// Session.
func Start(ctx context.Context, program string, args []string, opts ...Option) (*Process, error) {
	o, err := applyOptions(opts)
	if err != nil {
		return nil, err
	}
	c, err := newConPTY(o.rows, o.cols)
	if err != nil {
		return nil, err
	}
	p := &Process{pc: c.pc}
	if err := p.create(program, args, o); err != nil {
		return nil, errors.Join(err, c.close())
	}
	_ = windows.CloseHandle(c.inRead)
	_ = windows.CloseHandle(c.outWrite)
	stream := &console{r: os.NewFile(uintptr(c.outRead), "conout"), w: os.NewFile(uintptr(c.inWrite), "conin"), p: p}
	if p.s, err = expect.NewSession(stream, o.sessionOptions()...); err != nil {
		return nil, errors.Join(err, stream.Close(), p.terminate())
	}
	p.stop = context.AfterFunc(ctx, func() { _ = p.terminate() })
	return p, nil
}

// Session returns the conversation with the child.
func (p *Process) Session() *expect.Session { return p.s }

// PID returns the child's process ID.
func (p *Process) PID() int { return p.pid }

// Terminal returns the pseudo console controls.
func (p *Process) Terminal() Terminal { return conTerminal{p: p} }

// Wait waits for the child to exit. A non-zero exit is an *ExitError.
func (p *Process) Wait() (ExitStatus, error) {
	p.once.Do(func() {
		if _, err := windows.WaitForSingleObject(p.proc, windows.INFINITE); err != nil {
			p.status, p.waitErr = ExitStatus{Code: -1}, fmt.Errorf("spawn: wait: %w", err)
			return
		}
		var code uint32
		if err := windows.GetExitCodeProcess(p.proc, &code); err != nil {
			p.status, p.waitErr = ExitStatus{Code: -1}, fmt.Errorf("spawn: exit code: %w", err)
			return
		}
		p.status = ExitStatus{Code: int(code)}
		if code != 0 {
			p.waitErr = &ExitError{Status: p.status}
		}
		if p.stop != nil {
			p.stop()
		}
	})
	return p.status, p.waitErr
}

// Close closes the Session and the pseudo console, terminates the job and
// waits for the child.
func (p *Process) Close() error {
	err := p.s.Close()
	_ = p.terminate()
	_, _ = p.Wait()
	p.closeMu.Lock()
	defer p.closeMu.Unlock()
	if !p.closed {
		p.closed = true
		_ = windows.CloseHandle(p.job)
		_ = windows.CloseHandle(p.proc)
	}
	return err
}

// String identifies the process.
func (p *Process) String() string { return fmt.Sprintf("spawn.Process(%d)", p.pid) }

func (p *Process) create(program string, args []string, o options) error {
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return fmt.Errorf("spawn: attributes: %w", err)
	}
	defer attrs.Delete()
	// The attribute value is the HPCON itself, not a pointer to it.
	hpc := *(*unsafe.Pointer)(unsafe.Pointer(&p.pc))
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, hpc, unsafe.Sizeof(p.pc)); err != nil {
		return fmt.Errorf("spawn: attributes: %w", err)
	}
	si := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(si))
	cmdline, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{program}, args...)))
	if err != nil {
		return fmt.Errorf("spawn: command line: %w", err)
	}
	var dir *uint16
	if o.dir != "" {
		if dir, err = windows.UTF16PtrFromString(o.dir); err != nil {
			return fmt.Errorf("spawn: dir: %w", err)
		}
	}
	env, err := environmentBlock(o.env)
	if err != nil {
		return err
	}
	var pi windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_SUSPENDED)
	if err := windows.CreateProcess(nil, cmdline, nil, nil, false, flags, env, dir, &si.StartupInfo, &pi); err != nil {
		return fmt.Errorf("spawn: start %s: %w", program, err)
	}
	defer windows.CloseHandle(pi.Thread)
	p.proc, p.pid = pi.Process, int(pi.ProcessId)
	if p.job, err = killOnCloseJob(pi.Process); err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		_ = windows.CloseHandle(pi.Process)
		return err
	}
	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		_ = p.terminate()
		return fmt.Errorf("spawn: resume: %w", err)
	}
	return nil
}

func (p *Process) terminate() error {
	p.closeMu.Lock()
	defer p.closeMu.Unlock()
	if p.closed || p.job == 0 {
		return nil
	}
	return windows.TerminateJobObject(p.job, 1)
}

func killOnCloseJob(proc windows.Handle) (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("spawn: job: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return 0, fmt.Errorf("spawn: job limits: %w", err)
	}
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		_ = windows.CloseHandle(job)
		return 0, fmt.Errorf("spawn: job assign: %w", err)
	}
	return job, nil
}

func environmentBlock(env []string) (*uint16, error) {
	if env == nil {
		return nil, nil
	}
	block, err := windows.UTF16FromString(strings.Join(env, "\x00") + "\x00")
	if err != nil {
		return nil, fmt.Errorf("spawn: environment: %w", err)
	}
	return &block[0], nil
}

// conPTY is a pseudo console with its pipe ends.
type conPTY struct {
	pc                windows.Handle
	inRead, inWrite   windows.Handle
	outRead, outWrite windows.Handle
}

func newConPTY(rows, cols int) (*conPTY, error) {
	c := &conPTY{}
	if err := windows.CreatePipe(&c.inRead, &c.inWrite, nil, 0); err != nil {
		return nil, fmt.Errorf("spawn: pipe: %w", err)
	}
	if err := windows.CreatePipe(&c.outRead, &c.outWrite, nil, 0); err != nil {
		_ = windows.CloseHandle(c.inRead)
		_ = windows.CloseHandle(c.inWrite)
		return nil, fmt.Errorf("spawn: pipe: %w", err)
	}
	size := windows.Coord{X: int16(cols), Y: int16(rows)}
	if err := windows.CreatePseudoConsole(size, c.inRead, c.outWrite, 0, &c.pc); err != nil {
		_ = c.closePipes()
		return nil, fmt.Errorf("spawn: pseudo console: %w", err)
	}
	return c, nil
}

func (c *conPTY) closePipes() error {
	return errors.Join(windows.CloseHandle(c.inRead), windows.CloseHandle(c.inWrite),
		windows.CloseHandle(c.outRead), windows.CloseHandle(c.outWrite))
}

func (c *conPTY) close() error {
	windows.ClosePseudoConsole(c.pc)
	return c.closePipes()
}

// console is the Session transport: the pseudo console's pipes. Close
// closes the pseudo console first so that its output pipe reaches EOF.
type console struct {
	r, w *os.File
	p    *Process
	once sync.Once
	err  error
}

func (c *console) Read(b []byte) (int, error)  { return c.r.Read(b) }
func (c *console) Write(b []byte) (int, error) { return c.w.Write(b) }

func (c *console) Close() error {
	c.once.Do(func() {
		windows.ClosePseudoConsole(c.p.pc)
		c.err = errors.Join(c.w.Close(), c.r.Close())
	})
	return c.err
}

type conTerminal struct{ p *Process }

func (t conTerminal) SetSize(rows, cols int) error {
	if rows <= 0 || cols <= 0 || rows > 0x7fff || cols > 0x7fff {
		return &expect.ConfigError{Option: "size", Reason: "out of range"}
	}
	return windows.ResizePseudoConsole(t.p.pc, windows.Coord{X: int16(cols), Y: int16(rows)})
}

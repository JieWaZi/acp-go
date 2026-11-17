//go:build darwin || linux

package agentclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// guardianLaunch contains only the original frozen launch, never a second provider configuration.
type guardianLaunch struct {
	// Executable is the resolved native executable.
	Executable string
	// Args is the exact original argument vector.
	Args []string
	// Env is the exact original environment.
	Env []string
	// Dir is the frozen native working directory.
	Dir string
}

// guardianFrame carries lifecycle facts over a private status pipe, never native stdout.
type guardianFrame struct {
	// Kind is started, exit, error, or closed.
	Kind string
	// Error is a sanitized lifecycle error, without launch arguments or environment.
	Error string
	// Code is the native exit code.
	Code int
	// Signal is the native exit signal.
	Signal string
}

// guardianProcess proxies one original owner; it never signals a historical PID or another group.
type guardianProcess struct {
	// control is the only host-side liveness writer; closing it requests the same native cleanup.
	control *os.File
	// once makes host lease revocation idempotent.
	once sync.Once
	// done closes after the guardian's status stream and native process have both ended.
	done chan struct{}
	// confirmed records the guardian's explicit full-tree cleanup receipt before done.
	confirmed bool
	// err records an unexpected owner failure before done.
	err error
}

func (p *Process) startGuardian(ctx context.Context) (bool, error) {
	config := p.spec.Guardian
	if config == nil {
		return false, nil
	}
	if !filepath.IsAbs(config.Executable) || config.DuplicateLease == nil {
		return true, errors.New("invalid process guardian")
	}
	lease, err := config.DuplicateLease()
	if err != nil {
		return true, processOperationError("duplicate process owner lease", err)
	}
	if lease == nil {
		return true, errors.New("process owner lease is missing")
	}
	defer lease.Close()
	var allocated []*os.File
	pipe := func() (*os.File, *os.File, error) {
		r, w, err := os.Pipe()
		if err == nil {
			allocated = append(allocated, r, w)
		}
		return r, w, err
	}
	defer func() {
		for _, f := range allocated {
			_ = f.Close()
		}
	}()
	controlRead, controlWrite, err := pipe()
	if err != nil {
		return true, err
	}
	statusRead, statusWrite, err := pipe()
	if err != nil {
		return true, err
	}
	childInput, stdin, err := pipe()
	if err != nil {
		return true, err
	}
	stdout, childOutput, err := pipe()
	if err != nil {
		return true, err
	}
	stderr, childDiagnostic, err := pipe()
	if err != nil {
		return true, err
	}
	cmd := exec.Command(config.Executable, config.Args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = childInput, childOutput, childDiagnostic
	cmd.ExtraFiles = []*os.File{controlRead, statusWrite, lease}
	// The guardian must survive the host's process-group shutdown; it owns the native group separately.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := ctx.Err(); err != nil {
		return true, err
	}
	if err := cmd.Start(); err != nil {
		return true, processOperationError("start process guardian", err)
	}
	for _, f := range []*os.File{controlRead, statusWrite, childInput, childOutput, childDiagnostic} {
		_ = f.Close()
	}
	allocated = nil // The proxy now owns all remaining descriptors, including partial startup.
	remote := &guardianProcess{control: controlWrite, done: make(chan struct{})}
	p.mu.Lock()
	p.child, p.stdin, p.stdout, p.stderr, p.remote = cmd.Process, stdin, stdout, stderr, remote
	p.mu.Unlock()
	go p.readDiagnostics(stderr)
	started := make(chan error, 1)
	go func() {
		defer close(remote.done)
		defer statusRead.Close()
		decoder := json.NewDecoder(statusRead)
		admitted, exited := false, false
		for {
			var frame guardianFrame
			if err := decoder.Decode(&frame); err != nil {
				if !errors.Is(err, io.EOF) {
					remote.err = errors.New("process guardian status failed")
				}
				break
			}
			switch frame.Kind {
			case "started":
				admitted = true
				var startErr error
				if frame.Error != "" {
					startErr = errors.New(frame.Error)
				}
				started <- startErr
			case "exit":
				p.exit = ProcessExit{Code: frame.Code, Signal: frame.Signal}
				if frame.Error != "" {
					p.exit.Err = errors.New(frame.Error)
				}
				exited = true
				close(p.reaped)
			case "closed":
				remote.confirmed = true
			case "error":
				remote.err = errors.New(frame.Error)
			}
		}
		if err := cmd.Wait(); err != nil && !remote.confirmed {
			remote.err = errors.New("process guardian exited without cleanup confirmation")
		}
		if !admitted {
			started <- errors.New("process guardian did not confirm launch")
		}
		if !exited {
			p.exit = ProcessExit{Code: -1, Err: errors.New("native exit was not confirmed by process guardian")}
			close(p.reaped)
		}
	}()
	stop := context.AfterFunc(ctx, remote.revoke)
	defer stop()
	if err := json.NewEncoder(controlWrite).Encode(guardianLaunch{Executable: p.spec.Executable, Args: p.spec.Args, Env: p.spec.Env, Dir: p.spec.Dir}); err != nil {
		remote.revoke()
		return true, errors.New("process guardian launch transfer failed")
	}
	select {
	case err := <-started:
		if !stop() && ctx.Err() != nil {
			return true, ctx.Err()
		}
		p.mu.Lock()
		p.started = err == nil
		p.mu.Unlock()
		return true, err
	case <-ctx.Done():
		remote.revoke()
		return true, ctx.Err()
	}
}

func (g *guardianProcess) revoke() { g.once.Do(func() { _ = g.control.Close() }) }
func (g *guardianProcess) cleanup(p *Process) error {
	g.revoke()
	_ = closeProcessFile(p.stdin)
	_ = closeProcessFile(p.stdout)
	<-g.done
	<-p.drained
	if !g.confirmed {
		return errors.Join(g.err, errors.New("process guardian resource release is unconfirmed"))
	}
	return nil
}

// RunProcessGuardian handles the private inherited descriptors before normal host initialization.
// It retains the kernel lease until the original Process confirms actual tree cleanup.
func RunProcessGuardian() int {
	control, status, lease := os.NewFile(3, "owner-control"), os.NewFile(4, "owner-status"), os.NewFile(5, "owner-lease")
	if control == nil || status == nil || lease == nil {
		return 2
	}
	for _, f := range []*os.File{control, status, lease} {
		syscall.CloseOnExec(int(f.Fd()))
	}
	defer control.Close()
	defer status.Close()
	defer lease.Close()
	if _, err := lease.Stat(); err != nil {
		return 2
	}
	decoder := json.NewDecoder(control)
	var launch guardianLaunch
	if err := decoder.Decode(&launch); err != nil {
		return 2
	}
	process, err := NewProcess(ProcessSpec{Executable: launch.Executable, Args: launch.Args, Env: launch.Env, Dir: launch.Dir})
	encoder := json.NewEncoder(status)
	if err != nil {
		_ = encoder.Encode(guardianFrame{Kind: "started", Error: "invalid native launch"})
		_ = encoder.Encode(guardianFrame{Kind: "closed"})
		return 1
	}
	process.forwarded = &forwardedProcessStreams{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}
	hostGone := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, control); close(hostGone) }()
	// Start and EOF cleanup serialize through the original Process admission lock.
	go func() { <-hostGone; _ = process.Close(context.Background()) }()
	err = process.Start(context.Background())
	frame := guardianFrame{Kind: "started"}
	if err != nil {
		frame.Error = "native launch failed"
	}
	_ = encoder.Encode(frame)
	if err == nil {
		select {
		case <-process.exitObserved:
		case <-hostGone:
		}
	}
	for {
		if err := process.Close(context.Background()); err == nil {
			break
		}
		_ = encoder.Encode(guardianFrame{Kind: "error", Error: "native process resources remain owned"})
		time.Sleep(100 * time.Millisecond)
	}
	if exit, err := process.Wait(context.Background()); err == nil {
		frame := guardianFrame{Kind: "exit", Code: exit.Code, Signal: exit.Signal}
		if exit.Err != nil {
			frame.Error = "native process exited unsuccessfully"
		}
		_ = encoder.Encode(frame)
	}
	_ = encoder.Encode(guardianFrame{Kind: "closed"})
	return 0
}

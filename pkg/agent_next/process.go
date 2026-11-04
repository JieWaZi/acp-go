package agentclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

var (
	// ErrProcessNotStarted means the owner has no native exit to await.
	ErrProcessNotStarted = errors.New("agent process not started")
	// ErrProcessAlreadyUsed means this owner cannot admit another launch.
	ErrProcessAlreadyUsed = errors.New("agent process owner already used")

	errProcessUnsupported = errors.New("agent process containment is unavailable on this platform")
	errBatchDelimiter     = errors.New("batch launch input contains an unsupported control delimiter")
)

// ProcessSpec describes a single launch. NewProcess snapshots all launch inputs.
type ProcessSpec struct {
	// Executable is a path or a name resolved against the frozen absolute PATH entries.
	Executable string
	// Args excludes argv[0].
	Args []string
	// Env is the full child environment; nil snapshots the host environment.
	// Windows also freezes the required SYSTEMROOT if Env omits it.
	Env []string
	// Dir is the child's working directory; empty snapshots the current directory.
	Dir string
	// Redact sanitizes each complete stderr record before retention. Nil retains no stderr.
	Redact func(string) string
}

// ProcessExit is the native outcome, separate from closing the owner's resources.
type ProcessExit struct {
	// Code is the native exit code, or -1 if it is unavailable.
	Code int
	// Signal names the native signal when the platform exposes one.
	Signal string
	// Err is the native wait error, including a nonzero exit.
	Err error
	// Diagnostic is the first sanitized crash record or the latest sanitized record.
	Diagnostic string
	// Tail is the latest sanitized record, even if Diagnostic prefers the earlier crash.
	Tail string
}

// Process owns one child, its pipes and its exit observation. It cannot restart.
type Process struct {
	// spec contains the frozen launch, including its resolved absolute executable.
	spec ProcessSpec
	// mu protects launch admission and stream publication.
	mu sync.Mutex
	// attempted admits one Start call.
	attempted bool
	// closing prevents Start after Close has been admitted.
	closing bool
	// ready is closed once the admitted start has published its outcome.
	ready chan struct{}
	// started reports whether Start completed successfully.
	started bool
	// child is the native process identity, even if post-spawn setup failed.
	child *os.Process
	// stdin is the parent-side writer; callers may close it to send EOF.
	stdin *os.File
	// stdout is the parent-side reader; the owner keeps it through native exit.
	stdout *os.File
	// stderr is the parent's diagnostic reader; it is never exposed to callers.
	stderr *os.File
	// tree manages the platform's process group or job.
	tree processTree
	// stopMu serializes native tree termination without holding admission state.
	stopMu sync.Mutex
	// stopped closes once tree termination was requested successfully.
	stopped chan struct{}
	// reaped closes after the leader has been reaped, publishing exit.
	reaped chan struct{}
	// exit holds the leader's native outcome without stderr.
	exit ProcessExit
	// drained closes when the diagnostic reader publishes the sanitized records.
	drained chan struct{}
	// diagnostic is the preferred sanitized record.
	diagnostic string
	// tail is the latest sanitized record.
	tail string
	// cleanup is the active or latest cleanup attempt, protected by mu.
	cleanup *processCleanup
}

// processCleanup is shared by callers closing the same native owner.
type processCleanup struct {
	// done publishes the result of this attempt.
	done chan struct{}
	// err records cleanup failure, not native exit.
	err error
	// abortDrain interrupts an abandoned drain after a Close caller times out.
	abortDrain chan struct{}
	// abortOnce closes abortDrain at most once.
	abortOnce sync.Once
}

// NewProcess freezes the launch without starting a child or allocating pipes.
func NewProcess(spec ProcessSpec) (*Process, error) {
	dir, err := filepath.Abs(spec.Dir)
	if err != nil {
		return nil, processOperationError("freeze agent process directory", err)
	}
	if spec.Env == nil {
		spec.Env = os.Environ()
	} else {
		spec.Env = append(make([]string, 0, len(spec.Env)), spec.Env...)
	}
	if runtime.GOOS == "windows" {
		hasSystemRoot := false
		for _, entry := range spec.Env {
			key, _, ok := strings.Cut(entry, "=")
			hasSystemRoot = hasSystemRoot || (ok && strings.EqualFold(key, "SYSTEMROOT"))
		}
		if !hasSystemRoot {
			spec.Env = append(spec.Env, "SYSTEMROOT="+os.Getenv("SYSTEMROOT"))
		}
	}
	spec.Dir = dir
	spec.Args = append([]string(nil), spec.Args...)
	spec.Executable, err = resolveProcessExecutable(spec.Executable, dir, spec.Env)
	if err != nil {
		return nil, err
	}
	return &Process{
		spec: spec, ready: make(chan struct{}), stopped: make(chan struct{}),
		reaped: make(chan struct{}), drained: make(chan struct{}),
	}, nil
}

// Executable returns the absolute executable selected for this launch.
func (p *Process) Executable() string { return p.spec.Executable }

func resolveProcessExecutable(name, dir string, env []string) (string, error) {
	if name == "" || strings.ContainsRune(name, 0) {
		return "", errors.New("agent process executable is missing or invalid")
	}
	candidates := make([]string, 0, 1)
	if filepath.IsAbs(name) {
		candidates = append(candidates, filepath.Clean(name))
	} else if filepath.Base(name) != name {
		if filepath.VolumeName(name) != "" {
			return "", errors.New("agent process executable uses an ambiguous drive-relative path")
		}
		candidates = append(candidates, filepath.Join(dir, name))
	} else {
		for _, entry := range filepath.SplitList(processEnvValue(env, "PATH")) {
			if filepath.IsAbs(entry) {
				candidates = append(candidates, filepath.Join(entry, name))
			}
		}
	}
	for _, path := range candidates {
		if executable := platformExecutable(path, env); executable != "" {
			return executable, nil
		}
	}
	return "", fmt.Errorf("resolve agent process executable: %w", exec.ErrNotFound)
}

func processEnvValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		k, value, valid := strings.Cut(env[i], "=")
		if valid && (k == key || (runtime.GOOS == "windows" && strings.EqualFold(k, key))) {
			return value
		}
	}
	return ""
}

// Start admits the one native launch. Once started, cancellation of ctx does
// not control the child; use Close to stop it.
func (p *Process) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.attempted || p.closing {
		p.mu.Unlock()
		return ErrProcessAlreadyUsed
	}
	if err := ctx.Err(); err != nil {
		p.mu.Unlock()
		return err
	}
	p.attempted = true
	p.mu.Unlock()
	defer close(p.ready)

	cmd := &exec.Cmd{Path: p.spec.Executable, Args: append([]string{p.spec.Executable}, p.spec.Args...), Dir: p.spec.Dir, Env: p.spec.Env}
	var files []*os.File
	pipe := func() (*os.File, *os.File, error) {
		read, write, err := platformProcessPipe()
		if err == nil {
			files = append(files, read, write)
		}
		return read, write, err
	}
	spawned := false
	defer func() {
		if !spawned {
			for _, file := range files {
				_ = file.Close()
			}
			_ = p.tree.release()
		}
	}()

	if err := p.tree.prepare(cmd); err != nil {
		return processOperationError("prepare agent process", err)
	}
	childInput, stdin, err := pipe()
	if err != nil {
		return processOperationError("allocate agent stdin", err)
	}
	stdout, childOutput, err := pipe()
	if err != nil {
		return processOperationError("allocate agent stdout", err)
	}
	stderr, childDiagnostic, err := pipe()
	if err != nil {
		return processOperationError("allocate agent stderr", err)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = childInput, childOutput, childDiagnostic
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return processOperationError("start agent process", err)
	}
	spawned = true
	_ = childInput.Close()
	_ = childOutput.Close()
	_ = childDiagnostic.Close()

	setupErr := p.tree.afterStart(cmd)
	p.mu.Lock()
	p.child, p.stdin, p.stdout, p.stderr = cmd.Process, stdin, stdout, stderr
	p.started = setupErr == nil
	p.mu.Unlock()
	go p.readDiagnostics(stderr)
	go p.reap(cmd)
	if setupErr != nil {
		// A partially started native process remains owned by p; Close can retry.
		_ = p.stop()
		return processOperationError("contain agent process", setupErr)
	}
	return nil
}

func (p *Process) reap(cmd *exec.Cmd) {
	observeErr := p.tree.observeExit(cmd.Process.Pid)
	if p.tree.usesLeaderIdentity() {
		// Keep the PID reserved until the group has been signaled. Reaping
		// first would let a late stop target a recycled process group ID.
		_ = p.stop()
		<-p.stopped
	}
	waitErr := cmd.Wait()
	exit := ProcessExit{Code: -1, Err: waitErr}
	if cmd.ProcessState != nil {
		exit.Code = cmd.ProcessState.ExitCode()
		exit.Signal = platformExitSignal(cmd.ProcessState)
	}
	if observeErr != nil {
		exit.Err = errors.Join(waitErr, processOperationError("observe agent process exit", observeErr))
	}
	p.exit = exit
	close(p.reaped)
	if !p.tree.usesLeaderIdentity() {
		_ = p.stop()
	}
}

func (p *Process) stop() error {
	p.stopMu.Lock()
	defer p.stopMu.Unlock()
	select {
	case <-p.stopped:
		return nil
	default:
	}
	if err := p.tree.stop(p.child); err != nil {
		return processOperationError("stop agent process tree", err)
	}
	close(p.stopped)
	return nil
}

func (p *Process) beginStop() (bool, error) {
	p.stopMu.Lock()
	defer p.stopMu.Unlock()
	select {
	case <-p.stopped:
		return false, nil
	default:
	}
	graceful, err := p.tree.terminate(p.child)
	if err != nil {
		return false, processOperationError("terminate agent process tree", err)
	}
	return graceful, nil
}

// Stdin returns the parent writer after successful Start. Closing it sends EOF;
// Close also closes it if the caller leaves it open.
func (p *Process) Stdin() io.WriteCloser {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started {
		return nil
	}
	return p.stdin
}

// Stdout returns the parent reader after successful Start. Read it through EOF
// before Close when trailing protocol frames matter.
func (p *Process) Stdout() io.ReadCloser {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started {
		return nil
	}
	return p.stdout
}

// Done closes when the leader is reaped. It says nothing about stdout
// consumption, diagnostic drain, or release of the owner's resources.
func (p *Process) Done() <-chan struct{} { return p.reaped }

// Wait observes the native exit and the diagnostic drain without closing stdout.
func (p *Process) Wait(ctx context.Context) (ProcessExit, error) {
	if err := p.awaitStart(ctx); err != nil {
		return ProcessExit{}, err
	}
	if err := awaitProcess(ctx, p.reaped); err != nil {
		return ProcessExit{}, err
	}
	if err := awaitProcess(ctx, p.drained); err != nil {
		return ProcessExit{}, err
	}
	exit := p.exit
	exit.Diagnostic, exit.Tail = p.diagnostic, p.tail
	return exit, nil
}

func (p *Process) awaitStart(ctx context.Context) error {
	p.mu.Lock()
	attempted := p.attempted
	p.mu.Unlock()
	if !attempted {
		return ErrProcessNotStarted
	}
	if err := awaitProcess(ctx, p.ready); err != nil {
		return err
	}
	if p.child == nil {
		return ErrProcessNotStarted
	}
	return nil
}

// Close ends this owner's native tree and closes the parent streams. A failed
// or timed-out call leaves the same owner available for another cleanup attempt.
func (p *Process) Close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	p.closing = true
	attempt := p.cleanup
	if attempt != nil {
		select {
		case <-attempt.done:
			if attempt.err != nil {
				attempt = nil
			}
		default:
		}
	}
	if attempt == nil {
		attempt = &processCleanup{done: make(chan struct{}), abortDrain: make(chan struct{})}
		p.cleanup = attempt
		go p.runCleanup(attempt)
	}
	p.mu.Unlock()
	if err := awaitProcess(ctx, attempt.done); err != nil {
		attempt.abortOnce.Do(func() { close(attempt.abortDrain) })
		return err
	}
	return attempt.err
}

func (p *Process) runCleanup(attempt *processCleanup) {
	defer close(attempt.done)
	err := p.awaitStart(context.Background())
	if errors.Is(err, ErrProcessNotStarted) {
		attempt.err = p.tree.release()
		return
	}
	pipeErr := closeProcessFile(p.stdin)
	graceful, err := p.beginStop()
	if err != nil {
		attempt.err = errors.Join(pipeErr, err)
		return
	}
	if graceful {
		// A provider may persist its transcript on EOF or SIGTERM. The owner
		// keeps the same bounded stop attempt even if a Close waiter detaches.
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-p.reaped:
		case <-timer.C:
		}
		timer.Stop()
	}
	if err := p.stop(); err != nil {
		attempt.err = err
		return
	}
	pipeErr = errors.Join(pipeErr, closeProcessFile(p.stdout))
	<-p.reaped
	select {
	case <-p.drained:
	case <-attempt.abortDrain:
		// An escaped descendant could still hold the writer. Do not
		// publish the partial record when interrupting the reader.
		pipeErr = errors.Join(pipeErr, closeProcessFile(p.stderr))
		<-p.drained
	}
	attempt.err = errors.Join(pipeErr, p.tree.release())
}

func awaitProcess(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func closeProcessFile(file *os.File) error {
	if err := file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		return processOperationError("close agent process pipe", err)
	}
	return nil
}

// Process startup errors must not include paths, argv, or environment contents.
func processOperationError(operation string, err error) error {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return fmt.Errorf("%s: %w", operation, errno)
	}
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, os.ErrNotExist, os.ErrPermission, errProcessUnsupported, errBatchDelimiter} {
		if errors.Is(err, known) {
			return fmt.Errorf("%s: %w", operation, known)
		}
	}
	return errors.New(operation + " failed")
}

const (
	processRecordLimit     = 64 << 10
	processDiagnosticLimit = 2_000
)

var (
	processCrashRecord      = regexp.MustCompile(`^(?:F[0-9]{4} [0-9]{2}:[0-9]{2}:[0-9]{2}|FATAL ERROR:|Fatal Python error:|\*\*\* SIG[A-Z]+ .*received)`)
	processCredentialRecord = regexp.MustCompile(`(?i)(?:\b(?:authorization|(?:api|access|secret|private|refresh|session|client)[_ -]?(?:key|token|secret)|token|password|passwd|cookie|credentials?)["']?\s*[:=]|\b(?:bearer|basic)\s+\S|private key(?:-----|\b)|https?://[^\s"']*(?:[?@#])|\b(?:sk|ghp|gho|xox[baprs])[-_][a-z0-9_-]{8,})`)
)

// RedactProcessDiagnostic conservatively suppresses records with common
// credential fields, authorization values, or sensitive URLs. Providers with
// additional credential formats can compose a stricter redactor.
func RedactProcessDiagnostic(record string) string {
	if processCredentialRecord.MatchString(record) {
		return ""
	}
	return record
}

func (p *Process) readDiagnostics(file *os.File) {
	defer close(p.drained)
	defer file.Close()
	if p.spec.Redact == nil {
		_, _ = io.Copy(io.Discard, file)
		return
	}
	var firstCrash, tail string
	parser := processRecords{emit: func(record string) {
		isCrash := processCrashRecord.MatchString(record)
		record = strings.TrimSpace(p.spec.Redact(record))
		if record == "" {
			return
		}
		if len(record) > processDiagnosticLimit {
			record = record[:processDiagnosticLimit]
		}
		record = strings.ToValidUTF8(record, "")
		tail = record
		if firstCrash == "" && isCrash {
			firstCrash = record
		}
	}}
	_, err := io.Copy(&parser, file)
	if err == nil {
		parser.finish()
	}
	p.tail = tail
	p.diagnostic = firstCrash
	if p.diagnostic == "" {
		p.diagnostic = tail
	}
}

// processRecords frames bounded diagnostic records after removing terminal
// escapes. Structured records keep parent keys and continuation lines together.
type processRecords struct {
	// emit is called only for complete, non-overflowed records.
	emit func(string)
	// buf holds the current record up to processRecordLimit bytes.
	buf []byte
	// dropping discards every remaining byte of an oversized record.
	dropping bool
	// escape tracks a terminal control sequence before record framing.
	escape byte
	// depth counts open brackets inside a structured record.
	depth int
	// inspect distinguishes indented util.inspect output from JSON framing.
	inspect bool
	// inspectLine defers the continuation decision until the next byte arrives.
	inspectLine bool
	// jsonRecord limits JSON strings to their actual double-quote delimiter.
	jsonRecord bool
	// quote tracks a string delimiter inside a structured record.
	quote byte
	// backslash tracks escaping within a quoted string.
	backslash bool
	// ambiguous holds an opener pending evidence that it starts structured text.
	ambiguous bool
	// opaqueBlock suppresses multiline encoded key material.
	opaqueBlock bool
	// beginMatch and endMatch track markers even after discarding record storage.
	beginMatch int
	endMatch   int
	// sawBegin and sawEnd mark a matched delimiter within this record.
	sawBegin bool
	sawEnd   bool
	// nextLine marks an ambiguous opener that spanned a line boundary.
	nextLine bool
	// indentation marks whitespace on the line after an ambiguous opener.
	indentation bool
	// boundary is the end of the opener's line if it proves to be ordinary prose.
	boundary int
}

func (r *processRecords) Write(p []byte) (int, error) {
	for _, b := range p {
		if !r.stripTerminal(b) {
			r.add(b)
		}
	}
	return len(p), nil
}

func (r *processRecords) stripTerminal(b byte) bool {
	switch r.escape {
	case 0:
		if b == 0x1b {
			r.escape = 1
			return true
		}
		return (b < ' ' && b != '\n' && b != '\t') || b == 0x7f
	case 1:
		switch b {
		case '[':
			r.escape = 2
		case ']', 'P', 'X', '^', '_':
			r.escape = 3
		default:
			if b >= 0x30 {
				r.escape = 0
			}
		}
	case 2:
		if b >= 0x40 && b <= 0x7e {
			r.escape = 0
		}
	case 3:
		if b == 0x1b {
			r.escape = 4
		} else if b == 0x07 {
			r.escape = 0
		}
	case 4:
		if b == '\\' {
			r.escape = 0
		} else {
			r.escape = 3
		}
	}
	return true
}

func (r *processRecords) add(b byte) {
	if r.inspectLine {
		r.inspectLine = false
		r.quote, r.backslash = 0, false
		if b != ' ' && b != '\t' && b != '}' && b != ']' {
			// An unindented line is independent even if the inspect dump was
			// truncated. Discard the incomplete dump before framing this byte.
			r.dropping = true
			r.publish()
		}
	}
	if r.ambiguous {
		switch b {
		case '\n':
			if !r.nextLine {
				r.boundary = len(r.buf)
			}
			r.nextLine, r.indentation = true, false
			r.append(b)
			return
		case ' ', '\t', '\r':
			if r.nextLine && b != '\r' {
				r.indentation = true
			}
			r.append(b)
			return
		}
		r.ambiguous = false
		switch {
		case b == '"' || b == '{' || b == '[':
			r.depth, r.jsonRecord = 1, true
		case r.indentation && b != '}' && b != ']':
			r.depth, r.inspect = 1, true
		case !r.nextLine && b != '}' && b != ']':
			// Inline non-JSON payloads have no proven indentation boundary.
			// Keep them whole so an unknown serializer cannot leak fragments.
			r.depth = 1
		case r.nextLine:
			if r.boundary <= len(r.buf) {
				r.buf = r.buf[:r.boundary]
			}
			r.publish()
		}
		r.nextLine, r.indentation = false, false
	}
	if r.depth > 0 {
		switch {
		case r.inspect && b == '\n':
			r.inspectLine = true
		case r.quote != 0:
			if r.backslash {
				r.backslash = false
			} else if b == '\\' {
				r.backslash = true
			} else if b == r.quote {
				r.quote = 0
			}
		case b == '"' || (!r.jsonRecord && (b == '\'' || b == '`')):
			r.quote = b
		case b == '{' || b == '[':
			r.depth++
		case b == '}' || b == ']':
			r.depth--
			if r.depth == 0 {
				r.inspect, r.jsonRecord = false, false
			}
		}
	} else {
		switch b {
		case '\n':
			r.publish()
			return
		case '{', '[':
			r.ambiguous = true
		}
	}
	r.append(b)
}

func (r *processRecords) append(b byte) {
	r.sawBegin = matchProcessMarker(b, "-----BEGIN ", &r.beginMatch) || r.sawBegin
	r.sawEnd = matchProcessMarker(b, "-----END ", &r.endMatch) || r.sawEnd
	if r.dropping {
		return
	}
	if len(r.buf) >= processRecordLimit {
		r.buf = r.buf[:0]
		r.dropping = true
		return
	}
	r.buf = append(r.buf, b)
}

func matchProcessMarker(b byte, marker string, matched *int) bool {
	switch {
	case b == marker[*matched]:
		*matched = *matched + 1
		if *matched == len(marker) {
			*matched = 0
			return true
		}
	case b == '-' && *matched == 5:
		// The last five bytes still match the common dashed prefix.
	case b == '-':
		*matched = 1
	default:
		*matched = 0
	}
	return false
}

func (r *processRecords) publish() {
	switch {
	case r.opaqueBlock || r.sawBegin:
		r.opaqueBlock = !r.sawEnd
	case !r.dropping:
		record := bytes.TrimSpace(r.buf)
		if len(record) > 0 && utf8.Valid(record) {
			r.emit(string(record))
		}
	}
	r.buf = r.buf[:0]
	r.dropping = false
	r.beginMatch, r.endMatch = 0, 0
	r.sawBegin, r.sawEnd = false, false
	r.depth, r.quote = 0, 0
	r.inspect, r.inspectLine, r.jsonRecord, r.backslash = false, false, false, false
	r.ambiguous, r.nextLine, r.indentation = false, false, false
	r.boundary = 0
}

func (r *processRecords) finish() {
	if r.depth == 0 && r.quote == 0 && r.escape == 0 && !r.ambiguous {
		r.publish()
	}
}

package agentclient

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type processTree struct {
	// job owns the child tree by kernel handle, independent of its leader PID.
	job windows.Handle
	// assigned is false until the suspended child is in the job.
	assigned bool
}

func platformExecutable(path string, env []string) string {
	isFile := func(path string) bool {
		info, err := os.Stat(path)
		return err == nil && info.Mode().IsRegular()
	}
	if filepath.Ext(path) != "" && isFile(path) {
		return path
	}
	extensions := processEnvValue(env, "PATHEXT")
	if extensions == "" {
		extensions = ".COM;.EXE;.BAT;.CMD"
	}
	for _, ext := range strings.Split(extensions, ";") {
		if ext != "" && !strings.ContainsAny(ext, `/\:`) {
			if ext[0] != '.' {
				ext = "." + ext
			}
			if isFile(path + ext) {
				return path + ext
			}
		}
	}
	return ""
}

func (tree *processTree) prepare(cmd *exec.Cmd) error {
	attrs := &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED, HideWindow: true}
	ext := strings.ToLower(filepath.Ext(cmd.Path))
	if ext == ".cmd" || ext == ".bat" {
		interpreter := processEnvValue(cmd.Env, "ComSpec")
		if interpreter == "" {
			dir, err := windows.GetSystemDirectory()
			if err != nil {
				return err
			}
			interpreter = filepath.Join(dir, "cmd.exe")
		}
		shell, err := resolveProcessExecutable(interpreter, cmd.Dir, cmd.Env)
		if err != nil {
			return err
		}
		if !strings.EqualFold(filepath.Ext(shell), ".exe") {
			return errors.New("invalid batch interpreter")
		}
		line, err := processBatchCommandLine(cmd.Path, cmd.Args[1:])
		if err != nil {
			return err
		}
		attrs.CmdLine = syscall.EscapeArg(shell) + " /d /v:off /s /c " + line
		cmd.Path, cmd.Args = shell, []string{shell}
	}
	cmd.SysProcAttr = attrs

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	tree.job = job
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	return err
}

func (tree *processTree) afterStart(cmd *exec.Cmd) error {
	pid := uint32(cmd.Process.Pid)
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(tree.job, process); err != nil {
		return err
	}
	tree.assigned = true
	// Go's StartProcess closes the initial thread handle. Recover it while
	// the only thread is still suspended, so no child can escape assignment.
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{}
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err := windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return err
		}
		defer windows.CloseHandle(thread)
		_, err = windows.ResumeThread(thread)
		return err
	}
	return errors.New("could not find the suspended agent thread")
}

func (*processTree) observeExit(int) error { return nil }

func (*processTree) usesLeaderIdentity() bool { return false }

func (*processTree) terminate(*os.Process) (bool, error) { return false, nil }

func (tree *processTree) stop(child *os.Process) error {
	if tree.assigned {
		return windows.TerminateJobObject(tree.job, 1)
	}
	// Assignment failed while the child was still suspended. It cannot have
	// descendants; the native handle remains safe across a simultaneous reap.
	err := child.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func (tree *processTree) release() error {
	if tree.job == 0 {
		return nil
	}
	if err := windows.CloseHandle(tree.job); err != nil {
		return processOperationError("release agent process job", err)
	}
	tree.job = 0
	return nil
}

func platformExitSignal(*os.ProcessState) string { return "" }

// processBatchCommandLine reuses the repository's pure batch quoting algorithm
// from pkg/nativeacp/command.go. npm local shims forward through a second parse.
func processBatchCommandLine(executable string, args []string) (string, error) {
	words := make([]string, 0, len(args)+1)
	if strings.ContainsAny(executable, "\r\n\x00") {
		return "", errBatchDelimiter
	}
	words = append(words, escapeProcessBatchSyntax(executable))
	path := strings.ToLower(strings.ReplaceAll(executable, "\\", "/"))
	shim := strings.Contains(path, "/node_modules/.bin/") && strings.HasSuffix(path, ".cmd")
	for _, arg := range args {
		if strings.ContainsAny(arg, "\r\n\x00") {
			return "", errBatchDelimiter
		}
		var quoted strings.Builder
		quoted.WriteByte('"')
		slashes := 0
		for _, c := range arg {
			if c == '\\' {
				slashes++
				continue
			}
			if c == '"' {
				quoted.WriteString(strings.Repeat("\\", 2*slashes+1))
			} else {
				quoted.WriteString(strings.Repeat("\\", slashes))
			}
			slashes = 0
			quoted.WriteRune(c)
		}
		quoted.WriteString(strings.Repeat("\\", 2*slashes))
		quoted.WriteByte('"')
		word := escapeProcessBatchSyntax(quoted.String())
		if shim {
			word = escapeProcessBatchSyntax(word)
		}
		words = append(words, word)
	}
	return `"` + strings.Join(words, " ") + `"`, nil
}

func escapeProcessBatchSyntax(value string) string {
	var escaped strings.Builder
	for _, c := range value {
		if strings.ContainsRune("()[]%!^\"`<>&|;, *?", c) {
			escaped.WriteByte('^')
		}
		escaped.WriteRune(c)
	}
	return escaped.String()
}

// Create the original handles non-inheritable. Go duplicates only the ends
// listed in Cmd.Stdin, Stdout, and Stderr for the child.
func platformProcessPipe() (*os.File, *os.File, error) {
	var read, write windows.Handle
	if err := windows.CreatePipe(&read, &write, nil, 0); err != nil {
		return nil, nil, err
	}
	return os.NewFile(uintptr(read), "agent-pipe-reader"), os.NewFile(uintptr(write), "agent-pipe-writer"), nil
}

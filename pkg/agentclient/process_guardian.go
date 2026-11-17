package agentclient

import "os"

// ProcessGuardian transfers native ownership to a private host subprocess on Unix.
// Windows retains its kernel Job owner; it does not use Unix descriptor inheritance.
type ProcessGuardian struct {
	// Executable is the absolute host executable implementing RunProcessGuardian.
	Executable string
	// Args selects the host's private guardian mode without launching its normal services.
	Args []string
	// DuplicateLease returns a close-only duplicate of the host's already-held kernel lease.
	// The duplicate is transferred before native launch and must never explicitly unlock it.
	DuplicateLease func() (*os.File, error)
}

// forwardedProcessStreams are only used by the guardian's original native owner.
type forwardedProcessStreams struct {
	// stdin is the original host's protocol reader.
	stdin *os.File
	// stdout is the original host's protocol writer.
	stdout *os.File
	// stderr is forwarded directly to the original host's diagnostic redactor.
	stderr *os.File
}

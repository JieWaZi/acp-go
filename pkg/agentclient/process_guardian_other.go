//go:build !darwin && !linux

package agentclient

import "context"

// guardianProcess is unused where the native kernel Job owns host-death termination.
type guardianProcess struct{}

func (*Process) startGuardian(context.Context) (bool, error) { return false, nil }
func (*guardianProcess) cleanup(*Process) error              { return errProcessUnsupported }

// RunProcessGuardian is only implemented for the supported Unix process owners.
func RunProcessGuardian() int { return 2 }

//go:build !windows

package updater

import "os/exec"

type commandProcessOwner struct{}

func newCommandProcessOwner(enabled bool) (*commandProcessOwner, error) {
	return nil, nil
}

func (*commandProcessOwner) Assign(cmd *exec.Cmd) error {
	return nil
}

func (*commandProcessOwner) Terminate() {}

func (*commandProcessOwner) Close() {}

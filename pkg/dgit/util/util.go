package util

import (
	"io"
	"io/ioutil"
	"os/exec"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

// DefaultExecCommand runs commands using exec.Cmd
var DefaultExecCommand Command

func init() {
	DefaultExecCommand = &Commander{}
}

func ResetDefaultExecCommand() {
	DefaultExecCommand = &Commander{}
}

// Command is an interface used to run commands. All packages should use this
// interface instead of calling exec.Cmd directly.
type Command interface {
	RunCommand(cmd *exec.Cmd, stdin io.Reader) ([]byte, []byte, error)
}

func RunCommand(cmd *exec.Cmd, stdin io.Reader) ([]byte, []byte, error) {
	return DefaultExecCommand.RunCommand(cmd, stdin)
}

// Commander is the exec.Cmd implementation of the Command interface
type Commander struct{}

// RunCommand runs an exec.Command, optionally reading from stdin and return
// the stdout, stderr, and error responses respectively.
func (*Commander) RunCommand(cmd *exec.Cmd, stdin io.Reader) ([]byte, []byte, error) {
	logrus.Debugf("Running command: %s", cmd.Args)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}

	if stdin != nil {
		stdinPipe, err := cmd.StdinPipe()
		if err != nil {
			return nil, nil, err
		}
		go func() {
			defer stdinPipe.Close()
			io.Copy(stdinPipe, stdin)
		}()
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, errors.Wrapf(err, "starting command %v", cmd)
	}

	stdout, err := ioutil.ReadAll(stdoutPipe)
	if err != nil {
		return nil, nil, err
	}
	stderr, err := ioutil.ReadAll(stderrPipe)
	if err != nil {
		return nil, nil, err
	}

	err = cmd.Wait()
	logrus.Debugf("Command output: stdout %s, stderr: %s, err: %v", string(stdout), string(stderr), err)

	return stdout, stderr, err
}

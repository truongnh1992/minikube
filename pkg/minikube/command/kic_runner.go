/*
Copyright 2019 The Kubernetes Authors All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package command

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"time"

	"github.com/golang/glog"
	"github.com/pkg/errors"
	"golang.org/x/crypto/ssh/terminal"
	"k8s.io/minikube/pkg/minikube/assets"
)

// kicRunner runs commands inside a container
// It implements the CommandRunner interface.
type kicRunner struct {
	nameOrID string
	ociBin   string
}

// NewKICRunner returns a kicRunner implementor of runner which runs cmds inside a container
func NewKICRunner(containerNameOrID string, oci string) Runner {
	return &kicRunner{
		nameOrID: containerNameOrID,
		ociBin:   oci, // docker or podman
	}
}

func (k *kicRunner) RunCmd(cmd *exec.Cmd) (*RunResult, error) {
	args := []string{
		"exec",
		// run with privileges so we can remount etc..
		"--privileged",
	}
	if cmd.Stdin != nil {
		args = append(args,
			"-i", // interactive so we can supply input
		)
	}
	// if the command is hooked to another processes's output we want a tty
	if isTerminal(cmd.Stderr) || isTerminal(cmd.Stdout) {
		args = append(args,
			"-t",
		)
	}

	for _, env := range cmd.Env {
		args = append(args, "-e", env)
	}
	// append container name to docker arguments. all subsequent args
	// appended will be passed to the container instead of docker
	args = append(
		args,
		k.nameOrID, // ... against the container
	)

	args = append(
		args,
		cmd.Args...,
	)
	oc := exec.Command(k.ociBin, args...)
	oc.Stdin = cmd.Stdin
	oc.Stdout = cmd.Stdout
	oc.Stderr = cmd.Stderr
	oc.Env = cmd.Env

	rr := &RunResult{Args: cmd.Args}

	var outb, errb io.Writer
	if oc.Stdout == nil {
		var so bytes.Buffer
		outb = io.MultiWriter(&so, &rr.Stdout)
	} else {
		outb = io.MultiWriter(oc.Stdout, &rr.Stdout)
	}

	if oc.Stderr == nil {
		var se bytes.Buffer
		errb = io.MultiWriter(&se, &rr.Stderr)
	} else {
		errb = io.MultiWriter(oc.Stderr, &rr.Stderr)
	}

	oc.Stdout = outb
	oc.Stderr = errb

	start := time.Now()

	err := oc.Run()
	elapsed := time.Since(start)
	if err == nil {
		// Reduce log spam
		if elapsed > (1 * time.Second) {
			glog.Infof("Done: %v: (%s)", oc.Args, elapsed)
		}
	} else {
		if exitError, ok := err.(*exec.ExitError); ok {
			rr.ExitCode = exitError.ExitCode()
		}
		err = errors.Wrapf(err, "command failed: %s", oc.Args)
	}
	return rr, err

}

// Copy copies a file and its permissions
func (k *kicRunner) Copy(f assets.CopyableFile) error {
	assetName := f.GetAssetName()
	if _, err := os.Stat(f.GetAssetName()); os.IsNotExist(err) {
		fc := make([]byte, f.GetLength()) // Read  asset file into a []byte
		if _, err := f.Read(fc); err != nil {
			return errors.Wrap(err, "can't copy non-existing file")
		} // we have a MemoryAsset, will write to disk before copying

		tmpFile, err := ioutil.TempFile(os.TempDir(), "tmpf-memory-asset")
		if err != nil {
			return errors.Wrap(err, "creating temporary file")
		}
		//  clean up the temp file
		defer os.Remove(tmpFile.Name())
		if _, err = tmpFile.Write(fc); err != nil {
			return errors.Wrap(err, "write to temporary file")
		}

		// Close the file
		if err := tmpFile.Close(); err != nil {
			return errors.Wrap(err, "close temporary file")
		}
		assetName = tmpFile.Name()

	}
	// based of format of "docker cp containerName:destination"
	destination := fmt.Sprintf("%s:%s/%s", k.nameOrID, f.GetTargetDir(), f.GetTargetName())
	// make sure dir exists inside the container
	if _, err := k.RunCmd(exec.Command("mkdir", "-p", f.GetTargetDir())); err != nil {
		return errors.Wrapf(err, "error making dir %s", f.GetTargetDir())
	}

	if out, err := exec.Command(k.ociBin, "cp", assetName, destination).CombinedOutput(); err != nil {
		return errors.Wrapf(err, "error copying %s into node, output: %s", f.GetAssetName(), string(out))
	}

	if _, err := k.RunCmd(exec.Command("chmod", f.GetPermissions(), f.GetTargetDir())); err != nil {
		return errors.Wrap(err, "failed to chmod file permissions")
	}
	return nil
}

// Remove removes a file
func (k *kicRunner) Remove(f assets.CopyableFile) error {
	return fmt.Errorf("not implemented yet for kic runner")
}

// isTerminal returns true if the writer w is a terminal
func isTerminal(w io.Writer) bool {
	if v, ok := (w).(*os.File); ok {
		return terminal.IsTerminal(int(v.Fd()))
	}
	return false
}

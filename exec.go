package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func execCmd(cmdString string) error {
	// Parse the command to execute it
	cmdElmnts := strings.Split(cmdString, " ")
	if len(cmdElmnts) == 0 {
		return fmt.Errorf("no cmd given")
	}

	// Get the current working directory
	pwd, err := os.Getwd()
	if err != nil {
		log.Warnf("couldn't get current working directory")
		return err
	}

	// Lookup the full path of the binary to be executed
	bin, err := exec.LookPath(cmdElmnts[0])
	if err != nil {
		log.Warnf("Failed to find bin %s", cmdElmnts[0])
		return err
	}

	// Pass stdin / stdout / stderr as proc attributes
	procAttr := os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
		Dir:   pwd,
	}

	log.Debugf("Going to run `%s ( %s ) %s`", cmdElmnts[0], bin, strings.Join(cmdElmnts[1:], " "))

	// Start the process
	proc, err = os.StartProcess(bin, cmdElmnts, &procAttr)
	if err != nil {
		log.Warnf("Failed to start process")
		return err
	}

	// Wait until the end
	state, err := proc.Wait()
	if err != nil {
		log.Warnf("Error while waiting for proc")
		return err
	}

	log.Debugf("Result : %s", state)

	return nil
}

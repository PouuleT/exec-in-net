package main

import (
	"os"
	"syscall"

	"github.com/vishvananda/netns"
)

// newNS will create a new named namespace
func newNS() (*netns.NsHandle, error) {
	// Create a new network namespace
	log.Debug("Create a new ns")
	newns, err := netns.New()
	if err != nil {
		log.Warn(err)
		return nil, err
	}

	src := "/proc/self/ns/net"
	target := nsPath

	log.Debugf("Create file %s", target)
	// Create an empty file
	file, err := os.Create(target)
	if err != nil {
		log.Warn(err)
		return nil, err
	}
	// And close it
	err = file.Close()
	if err != nil {
		log.Warn(err)
		return nil, err
	}

	log.Debugf("Mount %s", target)

	// Mount the netnsPath just like iproute2 does
	if err := syscall.Mount(src, target, "none", syscall.MS_BIND, ""); err != nil {
		return nil, err
	}

	return &newns, nil
}

func getOriginalNS() (*netns.NsHandle, error) {
	// Save the current network namespace
	originalNS, err := netns.Get()
	if err != nil {
		log.Warn("panic when getting netns: ", err)
		return nil, err
	}
	return &originalNS, nil
}

// deleteNS will delete the given namespace
func deleteNS(ns *netns.NsHandle) error {
	log.Debugf("Deleting namespace")

	// Close the nsHandler
	err := ns.Close()
	if err != nil {
		log.Warn("Error while closing the namespace: ", err)
		return err
	}

	target := nsPath

	// Unmount the named namespace
	log.Debugf("Unmounting %s", target)
	if err := syscall.Unmount(target, 0); err != nil {
		log.Warnf("Error while unmounting %s : %s", target, err)
		return err
	}

	// Delete the namespace file
	log.Debugf("Deleting %s", target)
	if err := os.Remove(target); err != nil {
		log.Warnf("Error while deleting %s : %s", target, err)
		return err
	}

	return nil
}

// setupNetnsDir check that /run/netns directory is already mounted
func setupNetnsDir() error {
	netnsPath := "/run/netns"
	// Check if the directory /run/netns exists
	_, err := os.Stat(netnsPath)
	if err == nil {
		return nil
	}
	// Check if the error is 'no such file'
	if !os.IsNotExist(err) {
		return err
	}

	log.Debugf("/run/netns doesn't exist, need to create it")

	// Creating the netns directory
	log.Debugf("Creating directory %s", netnsPath)

	// Create the netnsPath just like iproute2 does
	return syscall.Mkdir(netnsPath, syscall.S_IRWXU|syscall.S_IRGRP|syscall.S_IXGRP|syscall.S_IROTH|syscall.S_IXOTH)
}

package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/Sirupsen/logrus"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

var ip, command, gateway, intf, logLevel, nsPath, mac string
var latency, jitter uint
var loss float64
var log = logrus.New()
var namespace, origns *netns.NsHandle
var proc *os.Process

func init() {
	flag.StringVar(&ip, "ip", "192.168.1.11/24", "IP network from where the command will be executed")
	flag.StringVar(&intf, "interface", "eth0", "interface used to get out of the network")
	flag.StringVar(&command, "command", "ip route", "command to be executed")
	flag.StringVar(&gateway, "gw", "", "gateway of the request (default will be the default route of the given interface)")
	flag.StringVar(&logLevel, "log-level", "info", "min level of logs to print")
	flag.StringVar(&mac, "mac", "", "mac address of the interface inside the namespace (default will be a random one)")
	flag.UintVar(&latency, "latency", 0, "latency added on the interface in ms")
	flag.UintVar(&jitter, "jitter", 0, "jitter added on the interface in ms")
	flag.Float64Var(&loss, "loss", 0, "loss added on the interface in percentage")
	flag.StringVar(
		&nsPath,
		"ns-path",
		"",
		"path of the temporary namespace to be created (default will be /var/run/netns/w000t$PID)",
	)
	flag.Parse()
}

func main() {
	lvl, err := logrus.ParseLevel(logLevel)
	if err != nil {
		logrus.Errorf("invalid log level %q: %q", logLevel, err)
		return
	}

	// Setup the logger
	log.Level = lvl
	log.Out = os.Stdout
	log.Formatter = &logrus.TextFormatter{
		FullTimestamp: true,
	}

	// Lock the OS Thread so we don't accidentally switch namespaces
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Save the current network namespace
	origns, err = getOriginalNS()
	if err != nil {
		log.Warn("panic when getting netns: ", err)
		return
	}
	defer origns.Close()

	// Check the /run/netns mount
	err = setupNetnsDir()
	if err != nil {
		log.Warn("Error setting up netns: ", err)
		return
	}

	eth, err := netlink.LinkByName(intf)
	if err != nil {
		log.Warnf("Error while getting %s : %s", intf, err)
		return
	}
	log.Debugf("%s : %+v", intf, eth.Attrs().Flags)

	// If no nsPath is given, we'll use one named like
	// /var/run/netns/w000t$PID
	if nsPath == "" {
		nsPath = fmt.Sprintf("/var/run/netns/w000t%d", os.Getpid())
	}

	// If no gateway is specified, we'll use the first route of the given interface
	if gateway == "" {
		routes, err := netlink.RouteList(eth, netlink.FAMILY_V4)
		if err != nil {
			log.Warn("Failed to get the route of the interface: ", err)
			return
		}

		for _, r := range routes {
			if r.Gw != nil {
				gateway = r.Gw.String()
				break
			}
		}
		if gateway == "" {
			log.Warnf("Couldn't find a default gateway for the specified interface")
			return
		}
	}
	gwaddr := net.ParseIP(gateway)

	// ============================== Create the macVLAN

	log.Debug("Create a new macVlan")

	macVlan := &netlink.Macvlan{
		LinkAttrs: netlink.LinkAttrs{
			Name:        "peth0",
			ParentIndex: eth.Attrs().Index,
			TxQLen:      -1,
		},
		Mode: netlink.MACVLAN_MODE_BRIDGE,
	}
	err = netlink.LinkAdd(macVlan)
	if err != nil {
		log.Warn("Error while creating macVlan: ", err)
		return
	}

	link, err := netlink.LinkByName("peth0")
	if err != nil {
		log.Warn("Error while getting macVlan: ", err)
		return
	}
	log.Debugf("MacVlan created : %+v", link)

	// If a mac was specified, set it now
	if mac != "" {
		log.Debugf("Setting macVlan with specified MAC : %s", mac)
		hardwareAddr, err := net.ParseMAC(mac)
		if err != nil {
			log.Warn("Error while parsing given mac: ", err)
			return
		}
		err = netlink.LinkSetHardwareAddr(link, hardwareAddr)
		if err != nil {
			log.Warn("Error while setting given mac on macVlan: ", err)
			return
		}
	}

	// ============================== Create the new Namespace

	namespace, err = newNS()
	if err != nil {
		log.Warn("error while creating new NS: ", err)
		return
	}
	defer deleteNS(namespace)

	log.Debug("Go back to original NS")

	err = netns.Set(*origns)
	if err != nil {
		log.Warn("Failed to change the namespace: ", err)
		return
	}

	// ============================== Add the MacVlan in the new Namespace

	log.Debug("Set the link in the NS")

	if err := netlink.LinkSetNsFd(link, int(*namespace)); err != nil {
		log.Warn("Could not attach to Network namespace: ", err)
		return
	}
	// ============================= Enter the new namespace to configure it

	log.Debug("Enter the namespace")

	err = netns.Set(*namespace)
	if err != nil {
		log.Warn("Failed to enter the namespace: ", err)
		return
	}

	// ============================= Configure the new namespace to configure it

	addr, err := netlink.ParseAddr(ip)
	if err != nil {
		log.Warn("Failed to parse the given IP: ", err)
		return
	}

	log.Debugf("Add the addr to the macVlan: %+v", addr)

	// ============================= Set the address in the namespace

	err = netlink.AddrAdd(link, addr)
	if err != nil {
		log.Warn("Failed to add the IP to the macVlan: ", err)
		return
	}

	log.Debug("Set the macVlan interface UP")

	// ============================= Set the link up in the namespace

	err = netlink.LinkSetUp(link)
	if err != nil {
		log.Warn("Error while setting up the interface peth0: ", err)
		return
	}

	// Check if we need to add Qdisc attributes
	if latency != 0 || jitter != 0 || loss != 0 {
		log.Debugf("Add TC : latency %d ms | jitter %d ms | loss %f", latency*1000, jitter*1000, loss)
		netem := netlink.NetemQdiscAttrs{
			Latency: uint32(latency) * 1000,
			Jitter:  uint32(jitter) * 1000,
			Loss:    float32(loss),
		}
		qdisc := netlink.NewNetem(
			netlink.QdiscAttrs{
				LinkIndex: link.Attrs().Index,
				Parent:    netlink.HANDLE_ROOT,
			},
			netem,
		)
		err = netlink.QdiscAdd(qdisc)
		if err != nil {
			log.Warn("Error while setting qdisc on macVlan: ", err)
			return
		}
	}

	log.Debugf("Set %s as the route", gwaddr)

	// ============================= Set the default route in the namespace

	err = netlink.RouteAdd(&netlink.Route{
		Scope:     netlink.SCOPE_UNIVERSE,
		LinkIndex: link.Attrs().Index,
		Gw:        gwaddr,
	})
	if err != nil {
		log.Warn("Error while setting up route on interface peth0 :", err)
		return
	}

	// ============================= Execute the command in the namespace

	err = execCmd(command)
	if err != nil {
		log.Warn("Error while running command : ", err)
	}

	log.Debug("Go back to orignal namspace")

	err = netns.Set(*origns)
	if err != nil {
		log.Warn("Error while going back to the original namespace: ", err)
		return
	}

	log.Debug("Exiting properly ...")
}

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
		Sys: &syscall.SysProcAttr{
			Setpgid: true,
		},
	}

	log.Debugf("Going to run `%s ( %s ) %s`", cmdElmnts[0], bin, strings.Join(cmdElmnts[1:], " "))

	// Create a channel that will listen to SIGINT / SIGTERM
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT)
	signal.Notify(c, syscall.SIGTERM)

	go func() {
		// Wait for a SIGINT / SIGTERM
		<-c
		log.Debugf("Got a SIGINT / SIGTERM")

		// Go back to the original namespace
		err := netns.Set(*origns)
		if err != nil {
			log.Warn("Failed to change the namespace: ", err)
		}

		// If there is a process, need to kill it
		if proc != nil {
			log.Debugf("Killing the process")
			err := proc.Kill()
			if err != nil {
				log.Warnf("error while killing proc", err)
			} else {
				log.Debugf("Killed the process")
			}
		} else {
			log.Debugf("No process to kill")
		}
	}()

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

// newNS will create a new named namespace
func newNS() (*netns.NsHandle, error) {
	pid := os.Getpid()

	// Create a new network namespace
	log.Debug("Create a new ns")
	newns, err := netns.New()
	if err != nil {
		log.Warn(err)
		return nil, err
	}

	src := fmt.Sprintf("/proc/%d/ns/net", pid)
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

	// Mount the namespace in /var/run/netns so it becomes a named namespace
	if err := syscall.Mount(src, target, "proc", syscall.MS_BIND|syscall.MS_NOEXEC|syscall.MS_NOSUID|syscall.MS_NODEV, ""); err != nil {
		return nil, err
	}

	return &newns, nil
}

func getOriginalNS() (*netns.NsHandle, error) {
	// Save the current network namespace
	origns, err := netns.Get()
	if err != nil {
		log.Warn("panic when getting netns: ", err)
		return nil, err
	}
	return &origns, nil
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
		log.Warn("Error while deleting %s : %s", target, err)
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
	err = os.Mkdir(netnsPath, os.ModePerm)
	if err != nil {
		return nil
	}

	// Mounting the netns directory
	log.Debugf("Mounting %s", netnsPath)
	if err := syscall.Mount("tmpfs", netnsPath, "tmpfs", syscall.MS_NOEXEC|syscall.MS_NOSUID|syscall.MS_NODEV, ""); err != nil {
		return err
	}
	return nil
}

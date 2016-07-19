package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

var ip, command, gateway, intf, logLevel string
var log = logrus.New()

func init() {
	flag.StringVar(&ip, "ip", "192.168.1.11/24", "IP network from where the command will be executed")
	flag.StringVar(&intf, "interface", "eth0", "interface used to get out of the network")
	flag.StringVar(&command, "command", "ip route", "command to be executed")
	flag.StringVar(&gateway, "gw", "", "gateway of the request")
	flag.StringVar(&logLevel, "log-level", "info", "min level of logs to print")
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
	origns, err := netns.Get()
	if err != nil {
		log.Warn("panic when getting netns: ", err)
		return
	}
	defer origns.Close()

	log.Debugf("Got original ns: %v\n", origns)

	// Check the /run/netns mount
	err = setupNetnsDir()
	if err != nil {
		log.Warn("Error setting up netns", err)
		return
	}

	eth, err := netlink.LinkByName(intf)
	if err != nil {
		log.Warnf("error while getting %s : %s", intf, err)
		return
	}
	log.Debugf("%s : %+v", intf, eth.Attrs().Flags)

	// askAndPrint()
	// ============================== Create the macVLAN

	log.Debug("Create a new macVlan")

	macVlan := &netlink.Macvlan{
		LinkAttrs: netlink.LinkAttrs{
			Name:        "peth0",
			ParentIndex: eth.Attrs().Index,
			TxQLen:      -1,
			// Namespace: int(*newns),
		},
		Mode: netlink.MACVLAN_MODE_BRIDGE,
	}
	err = netlink.LinkAdd(macVlan)
	if err != nil {
		log.Warn("Error while creating macVlan: ", err)
		return
	}

	err = netlink.LinkSetDown(macVlan)
	if err != nil {
		log.Warn("Error while setting macVlan down: ", err)
		return
	}

	link, err := netlink.LinkByName("peth0")
	if err != nil {
		log.Warn("error while getting macVlan :", err)
		return
	}
	log.Debugf("MacVlan created : %+v", link)

	// askAndPrint()
	// ============================== Create the new Namespace

	newns, err := newNS()
	if err != nil {
		log.Warn("error while creating new NS :", err)
		return
	}
	defer delNS(newns)

	log.Debug("Go back to original NS")

	netns.Set(origns)

	// askAndPrint()

	// ============================== Add the MacVlan in the new Namespace

	log.Debug("Set the link in the NS")

	if err := netlink.LinkSetNsFd(link, int(*newns)); err != nil {
		log.Warn("Could not attach to Network namespace: ", err)
		return
	}
	log.Debug("Done")

	// askAndPrint()
	// ============================= Enter the new namespace to configure it

	log.Debug("Enter the namespace")

	netns.Set(*newns)

	log.Debug("Done")

	// askAndPrint()
	// ============================= Configure the new namespace to configure it

	// // Get the loopback interface
	// lo, err := netlink.LinkByName("lo")
	// if err != nil {
	// 	log.Println("error while getting lo :", err)
	// 	return
	// }
	//
	// log.Println("Set up lo")
	// err = netlink.LinkSetUp(lo)
	// if err != nil {
	// 	log.Println("Error while setting up the interface lo", err)
	// 	return
	// }

	addr, err := netlink.ParseAddr(ip)
	if err != nil {
		log.Warn("Failed to parse ip", err)
		return
	}

	log.Debugf("Parsed the addr: %+v", addr)
	log.Debug("Add addr to peth0")
	netlink.AddrAdd(link, addr)
	gwaddr := net.ParseIP(gateway)

	log.Debug("Set up the peth0 interface")
	err = netlink.LinkSetUp(link)
	if err != nil {
		log.Warn("Error while setting up the interface peth0", err)
		return
	}

	log.Debugf("Set %s as the route", gwaddr)
	err = netlink.RouteAdd(&netlink.Route{
		Scope:     netlink.SCOPE_UNIVERSE,
		LinkIndex: link.Attrs().Index,
		Gw:        gwaddr,
	})
	if err != nil {
		log.Warn("Error while setting up route on interface peth0", err)
		return
	}

	log.Debug("Done")

	// log.Println("Testing request in origin namspace")
	// netns.Set(origns)
	// askAndPrint()
	//
	// err = execCmd(command)
	// if err != nil {
	// 	log.Println("error while checking IP")
	// }
	//
	// log.Println("Testing request in new namspace")
	//
	// netns.Set(*newns)
	// askAndPrint()

	err = execCmd(command)
	if err != nil {
		log.Warn("error while checking IP", err)
	}

	// askAndPrint()

	// log.Println("Testing request in origin namspace")
	// netns.Set(origns)
	// askAndPrint()

	// err = execCmd(command)
	// if err != nil {
	// 	log.Println("error while checking IP")
	// }

	// log.Println("Testing request in new namspace")
	//
	// netns.Set(*newns)
	// askAndPrint()
	//
	// err = execCmd(command)
	// if err != nil {
	// 	log.Println("error while checking IP")
	// }
	// ============================= Go back to original namespace

	log.Debug("Go back to orignal namspace")

	netns.Set(origns)

	log.Debug("Done")

	askAndPrint()
	log.Debug("Cleaning ...")
}

func checkIP() error {
	// Set the HTTP client
	client := http.DefaultClient
	client.Timeout = 1 * time.Second
	url := "http://ifconfig.ovh"

	// Do the request
	resp, err := client.Get(url)
	if err != nil {
		log.Warn("error while making get request", err)
		return err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	log.Debug("body : ", string(body))

	return nil
}

func execCmd(cmdString string) error {
	cmdElmnts := strings.Split(cmdString, " ")
	if len(cmdElmnts) == 0 {
		return fmt.Errorf("no cmd given")
	}

	cmd := exec.Command(cmdElmnts[0], cmdElmnts[1:]...)

	stdoutReader, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	defer stdoutReader.Close()

	if err := cmd.Start(); err != nil {
		return err
	}

	log.Infof("Output is :\n")
	io.Copy(os.Stdout, stdoutReader)

	if err := cmd.Wait(); err != nil {
		return err
	}

	return nil
}

func askAndPrint() {
	reader := bufio.NewReader(os.Stdin)
	log.Debug("===================================================")
	ifaces, err := net.Interfaces()
	if err != nil {
		log.Panic("panic when getting interfaces", err)
	}
	log.Debug("Got the interfaces :", ifaces)
	log.Debug("===================================================")
	reader.ReadString('\n')
}

// newNS will create a new named namespace
func newNS() (*netns.NsHandle, error) {
	log.Debug("in newNS")
	pid := os.Getpid()

	log.Debug("Create a new ns")
	newns, err := netns.New()
	if err != nil {
		log.Warn(err)
		return nil, err
	}
	log.Debugf("New netns: %v\n", newns)

	src := fmt.Sprintf("/proc/%d/ns/net", pid)
	target := getNsName()

	// Create an empty file
	file, err := os.Create(target)
	if err != nil {
		log.Warn(err)
		return nil, err
	}
	// And close it
	file.Close()

	log.Debugf("Created file %s\n", target)

	if err := syscall.Mount(src, target, "proc", syscall.MS_BIND|syscall.MS_NOEXEC|syscall.MS_NOSUID|syscall.MS_NODEV, ""); err != nil {
		return nil, err
	}
	log.Debugf("Mounted %s\n", target)

	log.Debug("All done")

	return &newns, nil
}

func getNsName() string {
	pid := os.Getpid()
	return fmt.Sprintf("/var/run/netns/w000t%d", pid)
}

func delNS(ns *netns.NsHandle) error {
	log.Debug("in delNS")
	// Close the nsHandler
	err := ns.Close()
	if err != nil {
		log.Warn("Error while closing", err)
		return err
	}

	target := getNsName()

	log.Debug("Unmounting")
	if err := syscall.Unmount(target, 0); err != nil {
		log.Warn("Error while unmounting", err)
		return err
	}
	log.Debugf("%s Unmounted", target)

	log.Debug("Deleting")
	if err := os.Remove(target); err != nil {
		log.Warn(err)
		return err
	}
	log.Debug("Deleted")

	// askAndPrint()

	return nil
}

// setupNetnsDir check that /run/netns directory is already mounted
func setupNetnsDir() error {
	netnsPath := "/run/netns"
	_, err := os.Stat(netnsPath)
	if err == nil {
		log.Debug("Nothing to do")
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	// log.Println("/run/netns doesn't exist, need to mount it")

	// log.Println("Creating directory")
	err = os.Mkdir(netnsPath, os.ModePerm)
	if err != nil {
		return nil
	}
	// log.Println("directory created")

	// log.Println("Mounting ...")
	if err := syscall.Mount("tmpfs", netnsPath, "tmpfs", syscall.MS_NOEXEC|syscall.MS_NOSUID|syscall.MS_NODEV, ""); err != nil {
		return err
	}
	// log.Printf("Mounted %s\n", netnsPath)
	return nil
}

// client, err := dhcp4client.New()
// if err != nil {
// 	log.Println("Error while creating new dhcp client", err)
// 	return
// }
// test, IP, err := client.Request()
// if err != nil {
// 	log.Println("Error while making dhcp request", err)
// 	return
// }
// if test {
// 	log.Println("Request success!")
// 	log.Printf("IP : %+v", IP)
// } else {
// 	log.Println("Request failed, no IP")
// }

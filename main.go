package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

var ip, command, gateway, intf string

func init() {
	flag.StringVar(&ip, "ip", "192.168.1.11/24", "IP network from where the command will be executed")
	flag.StringVar(&command, "command", "ip route", "command to be executed")
	flag.StringVar(&gateway, "gw", "", "gateway of the request")
	flag.StringVar(&intf, "interface", "eth0", "interface used to get out of the network")
	flag.Parse()
}

func main() {
	// Lock the OS Thread so we don't accidentally switch namespaces
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Save the current network namespace
	origns, err := netns.Get()
	if err != nil {
		log.Println("panic when getting netns: ", err)
		return
	}
	defer origns.Close()

	fmt.Printf("Got original ns: %v\n", origns)

	// Check the /run/netns mount
	err = setupNetnsDir()
	if err != nil {
		log.Println("Error setting up netns", err)
		return
	}

	eth, err := netlink.LinkByName(intf)
	if err != nil {
		log.Printf("error while getting %s : %s", intf, err)
		return
	}
	log.Printf("%s : %+v", intf, eth.Attrs().Flags)

	// Get the loopback interface
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		log.Fatal("error while getting lo :", err)
	}

	askAndPrint()
	// ============================== Create the new Namespace

	newns, err := newNS()
	if err != nil {
		log.Fatal("error while creating new NS :", err)
	}
	defer delNS(newns)

	log.Println("Go back to original NS")

	netns.Set(origns)

	askAndPrint()
	// ============================== Create the macVLAN

	log.Println("Create a new macVlan")

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
		log.Println("Error while creating macVlan: ", err)
		return
	}

	link, err := netlink.LinkByName("peth0")
	if err != nil {
		log.Fatal("error while getting macVlan :", err)
	}
	log.Printf("MacVlan created : %+v", link)

	askAndPrint()

	// ============================== Add the MacVlan in the new Namespace

	if err := netlink.LinkSetNsFd(link, int(*newns)); err != nil {
		log.Println("Could not attach to Network namespace: ", err)
		return
	}
	log.Println("Done")

	askAndPrint()
	// ============================= Enter the new namespace to configure it

	log.Println("Enter the namespace")

	netns.Set(*newns)

	log.Println("Done")

	askAndPrint()
	// ============================= Configure the new namespace to configure it

	err = netlink.LinkSetUp(lo)
	if err != nil {
		log.Println("Error while setting up the interface lo", err)
		return
	}

	addr, _ := netlink.ParseAddr(ip)
	log.Printf("Parsed the addr: %+v", addr)
	netlink.AddrAdd(link, addr)
	gwaddr := net.ParseIP(gateway)

	log.Println("Set up the peth0 interface")
	err = netlink.LinkSetUp(link)
	if err != nil {
		log.Println("Error while setting up the interface peth0", err)
		return
	}

	log.Printf("Set %s as the route", gwaddr)
	err = netlink.RouteAdd(&netlink.Route{
		Scope:     netlink.SCOPE_UNIVERSE,
		LinkIndex: link.Attrs().Index,
		Gw:        gwaddr,
	})
	if err != nil {
		log.Println("Error while setting up route on interface peth0", err)
		return
	}

	log.Println("Done")

	log.Println("Testing request in origin namspace")
	askAndPrint()

	netns.Set(origns)
	//
	err = execCmd(command)
	if err != nil {
		log.Println("error while checking IP")
	}

	log.Println("Testing request in new namspace")

	askAndPrint()

	netns.Set(*newns)

	err = execCmd(command)
	if err != nil {
		log.Println("error while checking IP", err)
	}

	askAndPrint()
	// ============================= Go back to original namespace

	log.Println("Go back to orignal namspace")

	netns.Set(origns)

	log.Println("Done")

	askAndPrint()
	log.Println("Cleaning ...")
}

func checkIP() error {
	// Set the HTTP client
	client := http.DefaultClient
	client.Timeout = 1 * time.Second
	url := "http://ifconfig.ovh"

	// Do the request
	resp, err := client.Get(url)
	if err != nil {
		log.Println("error while making get request", err)
		return err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	log.Println("body : ", string(body))

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
		log.Println("Error while running cmd", err)
		return err
	}
	defer stdoutReader.Close()

	if err := cmd.Start(); err != nil {
		log.Println("Error while running cmd", err)
		return err
	}

	log.Printf("Output is :\n")
	io.Copy(os.Stdout, stdoutReader)

	if err := cmd.Wait(); err != nil {
		log.Println("Error while running cmd", err)
		return err
	}

	return nil
}

func askAndPrint() {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("===================================================")
	ifaces, err := net.Interfaces()
	if err != nil {
		log.Panic("panic when getting interfaces", err)
	}
	log.Println("Got the interfaces :", ifaces)
	fmt.Println("===================================================")
	reader.ReadString('\n')
	//
}

// newNS will create a new named namespace
func newNS() (*netns.NsHandle, error) {
	log.Println("in newNS")
	pid := os.Getpid()

	log.Println("Create a new ns")
	newns, err := netns.New()
	if err != nil {
		log.Println(err)
		return nil, err
	}
	fmt.Printf("New netns: %v\n", newns)

	src := fmt.Sprintf("/proc/%d/ns/net", pid)
	target := getNsName()

	// Create an empty file
	file, err := os.Create(target)
	if err != nil {
		log.Println(err)
		return nil, err
	}
	// And close it
	file.Close()

	fmt.Printf("Created file %s\n", target)

	if err := syscall.Mount(src, target, "proc", syscall.MS_BIND|syscall.MS_NOEXEC|syscall.MS_NOSUID|syscall.MS_NODEV, ""); err != nil {
		log.Println(err)
		return nil, err
	}
	log.Printf("Mounted %s\n", target)

	log.Println("All done")

	return &newns, nil
}

func getNsName() string {
	pid := os.Getpid()
	return fmt.Sprintf("/var/run/netns/w000t%d", pid)
}

func delNS(ns *netns.NsHandle) error {
	// log.Println("in delNS")
	// Close the nsHandler
	err := ns.Close()
	if err != nil {
		log.Println("Error while closing", err)
		return err
	}

	target := getNsName()

	// log.Println("Unmounting")
	if err := syscall.Unmount(target, 0); err != nil {
		log.Println(err)
		return err
	}
	// log.Printf("%s Unmounted", target)

	// log.Println("Deleting")
	if err := os.Remove(target); err != nil {
		log.Println(err)
		return err
	}
	// log.Println("Deleted")

	// askAndPrint()

	return nil
}

// setupNetnsDir check that /run/netns directory is already mounted
func setupNetnsDir() error {
	netnsPath := "/run/netns"
	_, err := os.Stat(netnsPath)
	if err == nil {
		log.Println("Nothing to do")
		return nil
	}
	if !os.IsNotExist(err) {
		log.Println("Error while testing existing file", err)
		return err
	}
	// log.Println("/run/netns doesn't exist, need to mount it")

	// log.Println("Creating directory")
	err = os.Mkdir(netnsPath, os.ModePerm)
	if err != nil {
		log.Println("Failed to mkdir", err)
		return nil
	}
	// log.Println("directory created")

	// log.Println("Mounting ...")
	if err := syscall.Mount("tmpfs", netnsPath, "tmpfs", syscall.MS_NOEXEC|syscall.MS_NOSUID|syscall.MS_NODEV, ""); err != nil {
		log.Printf("error while mounting %s : %s", netnsPath, err)
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

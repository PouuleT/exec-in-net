package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"runtime"
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
var done = make(chan struct{})
var err error
var eth netlink.Link
var gwaddr net.IP
var addr *netlink.Addr

func init() {
	// Parse the arguments
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

func initVar() error {
	lvl, err := logrus.ParseLevel(logLevel)
	if err != nil {
		logrus.Errorf("invalid log level %q: %q", logLevel, err)
		return err
	}

	// Setup the logger
	log.Level = lvl
	log.Out = os.Stdout
	log.Formatter = &logrus.TextFormatter{
		FullTimestamp: true,
	}

	// Check the /run/netns mount
	err = setupNetnsDir()
	if err != nil {
		log.Warn("Error setting up netns: ", err)
		return err
	}

	// Get the link
	eth, err = netlink.LinkByName(intf)
	if err != nil {
		log.Warnf("Error while getting %s : %s", intf, err)
		return err
	}
	log.Debugf("%s : %+v", intf, eth.Attrs().Flags)

	// If no nsPath is given, we'll use one named like
	// /var/run/netns/w000t$PID
	if nsPath == "" {
		nsPath = fmt.Sprintf("/var/run/netns/w000t%d", os.Getpid())
	}

	// If no gateway is specified, we'll use the first route of the given interface
	if gateway == "" {
		// Get the routes
		routes, err := netlink.RouteList(eth, netlink.FAMILY_V4)
		if err != nil {
			log.Warn("Failed to get the route of the interface: ", err)
			return err
		}

		for _, r := range routes {
			if r.Gw != nil {
				gateway = r.Gw.String()
				break
			}
		}
		if gateway == "" {
			return fmt.Errorf("Couldn't find a default gateway for the specified interface")
		}
	}
	gwaddr = net.ParseIP(gateway)

	// Parse the IP
	addr, err = netlink.ParseAddr(ip)
	if err != nil {
		log.Warn("Failed to parse the given IP: ", err)
		return err
	}
	return nil
}

func main() {
	// Lock the OS Thread so we don't accidentally switch namespaces
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Get the current network namespace
	origns, err = getOriginalNS()
	if err != nil {
		panic(fmt.Sprintf("panic when getting netns: %s", err))
	}
	defer origns.Close()

	// Init de main variables
	err = initVar()
	if err != nil {
		panic(fmt.Sprintf("panic when initializing vars: %s", err))
	}

	// ============================== Create the macVLAN

	log.Debug("Create a new macVlan")

	// Create the new macVlan interface
	link, err := newMacVLAN()
	if err != nil {
		log.Warn("Error while creating macVlan: ", err)
		return
	}

	// ============================= Set the mac address to the macVlan interface

	err = setMacVlanMacAddr(link)
	if err != nil {
		log.Warn("Error while setting vlan mac: ", err)
		return
	}

	// ============================== Create the new Namespace

	namespace, err = newNS()
	if err != nil {
		log.Warn("error while creating new NS: ", err)
		return
	}
	defer deleteNS(namespace)

	log.Debug("Go back to original NS : ", int(*origns))

	err = netns.Set(*origns)
	if err != nil {
		log.Warn("Failed to change the namespace: ", err)
		return
	}

	// ============================== Add the MacVlan in the new Namespace

	log.Debug("Set the link in the NS")

	if err := netlink.LinkSetNsFd(*link, int(*namespace)); err != nil {
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

	log.Debugf("Add the addr to the macVlan: %+v", addr)

	// ============================= Set the address in the namespace

	err = netlink.AddrAdd(*link, addr)
	if err != nil {
		log.Warn("Failed to add the IP to the macVlan: ", err)
		return
	}

	log.Debug("Set the macVlan interface UP")

	// ============================= Set the link up in the namespace

	err = netlink.LinkSetUp(*link)
	if err != nil {
		log.Warn("Error while setting up the interface peth0: ", err)
		return
	}

	err = setTcAttributes(link)
	if err != nil {
		log.Warn("Error while setting up the interface TC attributes: ", err)
		return
	}
	log.Debugf("Set %s as the route", gwaddr)

	// ============================= Set the default route in the namespace

	err = setLinkRoute(link)
	if err != nil {
		log.Warn("Error while setting up the interface route: ", err)
		return
	}
	// ============================= Execute the command in the namespace

	// Create a channel that will listen to SIGINT / SIGTERM
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT)
	signal.Notify(c, syscall.SIGTERM)

	// Wait for the process to end
	go func() {
		for {
			select {
			// If we recieve a signal, we let it flow to the process
			case signal := <-c:
				log.Debugf("Got a %s", signal.String())
			// If the process is done, we exit properly
			case <-done:
				log.Debugf("Process exited properly, exiting")
				return
			}
		}
	}()

	// Launch the command in a go routine
	err = execCmd(command)
	if err != nil {
		log.Warn("Error while running command : ", err)
	}
	done <- struct{}{}

	log.Debug("Go back to orignal namspace")

	err = netns.Set(*origns)
	if err != nil {
		log.Warn("Error while going back to the original namespace: ", err)
		return
	}

	log.Debug("Exiting properly ...")
}

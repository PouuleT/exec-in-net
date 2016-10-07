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

func newMacVLAN() (*netlink.Link, error) {
	macVlan := &netlink.Macvlan{
		LinkAttrs: netlink.LinkAttrs{
			Name:        "peth0",
			ParentIndex: eth.Attrs().Index,
			TxQLen:      -1,
		},
		Mode: netlink.MACVLAN_MODE_BRIDGE,
	}
	// Creat the macVLAN
	err = netlink.LinkAdd(macVlan)
	if err != nil {
		log.Warn("Error while creating macVlan: ", err)
		return nil, err
	}

	// Retrieve the newly created macVLAN
	link, err := netlink.LinkByName("peth0")
	if err != nil {
		log.Warn("Error while getting macVlan: ", err)
		return nil, err
	}
	log.Debugf("MacVlan created : %+v", link)

	return &link, err
}

func initVar() {

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

	// Check the /run/netns mount
	err = setupNetnsDir()
	if err != nil {
		log.Warn("Error setting up netns: ", err)
		return
	}

	eth, err = netlink.LinkByName(intf)
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
	gwaddr = net.ParseIP(gateway)

	addr, err = netlink.ParseAddr(ip)
	if err != nil {
		log.Warn("Failed to parse the given IP: ", err)
		return
	}
	return
}

func setMacVlanMacAddr(link *netlink.Link) error {
	// If a mac was specified, set it now
	if mac != "" {
		log.Debugf("Setting macVlan with specified MAC : %s", mac)
		hardwareAddr, err := net.ParseMAC(mac)
		if err != nil {
			log.Warn("Error while parsing given mac: ", err)
			return err
		}
		err = netlink.LinkSetHardwareAddr(*link, hardwareAddr)
		if err != nil {
			log.Warn("Error while setting given mac on macVlan: ", err)
			return err
		}
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

	log.Println("Got original ns : ", int(*origns))

	// Init de main variables
	initVar()

	// ============================== Create the macVLAN

	log.Debug("Create a new macVlan")
	log.Println("Got original ns : ", int(*origns))

	// Create the new macVlan interface
	link, err := newMacVLAN()
	if err != nil {
		log.Warn("Error while creating macVlan: ", err)
		return
	}

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
				LinkIndex: (*link).Attrs().Index,
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
		LinkIndex: (*link).Attrs().Index,
		Gw:        gwaddr,
	})
	if err != nil {
		log.Warn("Error while setting up route on interface peth0 :", err)
		return
	}

	// ============================= Execute the command in the namespace

	// Create a channel that will listen to SIGINT / SIGTERM
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT)
	signal.Notify(c, syscall.SIGTERM)

	go func() {
		err = execCmd(command)
		if err != nil {
			log.Warn("Error while running command : ", err)
		}
		done <- struct{}{}
	}()

	// Wait for the process to end
FOR_LOOP:
	for {
		select {
		// If we recieve a signal, we let it flow to the process
		case signal := <-c:
			log.Debugf("Got a %s", signal.String())
		// If the process is done, we exit properly
		case <-done:
			log.Debugf("Process exited properly, exiting")
			break FOR_LOOP
		}
	}

	log.Debug("Go back to orignal namspace")

	err = netns.Set(*origns)
	if err != nil {
		log.Warn("Error while going back to the original namespace: ", err)
		return
	}

	log.Debug("Exiting properly ...")
}

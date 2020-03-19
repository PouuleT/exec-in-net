package main

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
)

func setTcAttributes(link *netlink.Link) error {
	// Check if we need to add Qdisc attributes
	if latency == 0 && jitter == 0 && loss == 0 {
		return nil
	}
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
		return err
	}
	return nil
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

func setLinkRoute(link *netlink.Link) error {
	// Add a route for the gateway
	_, gwaddrNet, err := net.ParseCIDR(fmt.Sprintf("%s/32", gwaddr.String()))
	if err != nil {
		log.Warnf("Error when parsing gateway: %s", err)
		return err
	}
	log.Debug("Setting a route for the gateway")
	err = netlink.RouteAdd(&netlink.Route{
		Scope:     netlink.SCOPE_LINK,
		LinkIndex: (*link).Attrs().Index,
		Dst:       gwaddrNet,
	})
	if err != nil {
		log.Warn("Error while setting link route: ", err)
		return err
	}

	log.Debugf("Set %s as the default gateway", gwaddr)
	// Add the default gateway
	return netlink.RouteAdd(&netlink.Route{
		Scope:     netlink.SCOPE_UNIVERSE,
		LinkIndex: (*link).Attrs().Index,
		Gw:        gwaddr,
	})
}

func setMacVlanMacAddr(link *netlink.Link) error {
	// If a mac was specified, set it now
	if mac == "" {
		return nil
	}
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
	return nil
}

func setMacVlanMTU(link *netlink.Link) error {
	// If a mtu was specified, set it now
	if mtu == 0 {
		return nil
	}
	log.Debugf("Setting macVlan with specified MTU : %d", mtu)
	err = netlink.LinkSetMTU(*link, mtu)
	if err != nil {
		log.Warn("Error while setting given mtu on macVlan: ", err)
		return err
	}
	return nil
}

# Exec in network

[![Build Status](https://github.com/PouuleT/exec-in-net/workflows/Build/badge.svg?branch=master)](https://github.com/PouuleT/exec-in-net/workflows/Build/badge.svg?branch=master) 
[![Go Report Card](https://goreportcard.com/badge/github.com/PouuleT/exec-in-net)](https://goreportcard.com/report/github.com/PouuleT/exec-in-net)

> Execute any commands within a network namespace with its own IP and route

## Usage

```
    -command string
          command to be executed (default "ip route")
    -gw string
          gateway of the request (default will be the default route of the given interface)
    -interface string
          interface used to get out of the network (default "eth0")
    -ip string
          IP network from where the command will be executed (default "192.168.1.11/24")
    -log-level string
          min level of logs to print (default "info")
    -mac string
          mac address of the interface inside the namespace (default will be a random one)
    -ns-path string
          path of the temporary namespace to be created (default will be /var/run/netns/w000t$PID)
    -loss float
          loss added on the interface in percentage (default 0)
    -jitter uint
          jitter added on the interface in ms (default 0)
    -mtu int
          MTU of the interface
    -latency uint
          latency added on the interface in ms (default 0)
```

## Examples

- Have multiple exit point on your network ?

> Do a curl using the specific gateway 192.168.1.1

```
./exec-in-net -gw 192.168.1.1 -command "curl ifconfig.ovh"
```

- Have multiple public IP on your host ?

> Do a curl from an interface using the specific IP 37.59.14.101

```
./exec-in-net -ip 37.59.14.101/32 -command "curl ifconfig.ovh"
```

- Want to simulate bad network environment ( packet loss / latency ) ?

> Send 30 pings to google, with 10% loss, an added network latency of 50ms and an added variable lattency of 20 ms (jitter)

```
./exec-in-net -command "ping -c 30 8.8.8.8" -loss 10 -latency 50 -jitter 20
```

# Exec in network

[![Build Status](https://travis-ci.org/PouuleT/exec-in-net.svg?branch=master)](https://travis-ci.org/PouuleT/exec-in-net)
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

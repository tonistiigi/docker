<!--[metadata]>
+++
title = "Setup for the tutorial"
description = "Getting Started tutorial for Docker Swarm"
keywords = ["tutorial, cluster management, swarm"]
[menu.main]
identifier="tutorial-setup"
parent="swarm-tutorial"
weight=11
+++
<![end-metadata]-->

## Getting Started with Docker Swarm
This tutorial introduces you to the key features of Docker Swarm. It guides you through the following activities:

* initializing a cluster of Docker Engines called a Swarm
* adding nodes to the Swarm
* deploying application services to the Swarm
* managing the Swarm once you have everything running

This tutorial uses Docker Engine CLI commands entered on the command line of a terminal window. You should be able to install Docker on networked machines and be comfortable running commands in the shell of your choice.

If you’re brand new to Docker, see [About Docker Engine](../../index.md).

## Setup ##
To run this tutorial, you need the following:

* 3 networked machines with Docker 1.12 or later installed

    These can be virtual machines on your PC, in a data center, or on a cloud service provider.

    For instructions on installing Docker Engine, see [Install Docker Engine](../../installation/index.md).

    To see an example on how to install Docker Engine on a cloud provider, go to [Example: Manual install on cloud provider](../../installation/cloud/cloud-ex-aws.md).

* the IP address of each of the machines you use for the tutorial

    The IP address must be assigned to an a network interface available to the host operating system. Run `ifconfig` to see a list of the available network interfaces:

    ```
    $ ifconfig

    ...snip...
    eth0: flags=4163<UP,BROADCAST,RUNNING,MULTICAST>  mtu 9001
        inet 168.0.32.137  netmask 255.255.0.0  broadcast 168.0.255.255
        inet6 fe80::d8:f5ff:fe48:cabb prefixlen 64  scopeid 0x20<link>
    ...snip...
    ```
    For the manager node, the ip address serves as the VXLAN tunnel endpoint (VTEP) for the Swarm.

    This tutorial uses the following host names and ip addresses :
    * `manager1` : `192.168.99.100`
    * `worker1` : `192.168.99.102`
    * `worker2` : `192.168.99.100`


* Docker recommends that every node in the cluster be on the same L3 subnet with all traffic permitted between nodes.

    If security policies require you to restrict traffic, you must open the following ports between nodes in the cluster:

    * **TCP port 2377** for cluster management communications
    * **TCP** and **UDP port 7946** for communication among nodes
    * **TCP** and **UDP port 4789** for overlay network traffic

<p style="margin-bottom:300px">&nbsp;</p>

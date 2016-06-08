<!--[metadata]>
+++
title = "Create a Swarm"
description = "Initialize the Swarm"
keywords = ["tutorial, cluster management, swarm"]
[menu.main]
identifier="initialize-swarm"
parent="swarm-tutorial"
weight=12
+++
<![end-metadata]-->

## Create a Swarm

After you install Docker on your networked machines and start the Docker Engine daemon, you're ready to create a Swarm. If you haven't already, read the through the [tutorial setup](tutorial-setup.md).

1. Open a terminal and ssh into the machine where you want to run your manager node. For example, the tutorial uses a machine named `manager1`.

2. To create a new Swarm, run the following command:
   ```
   $ docker swarm init --listen-addr MANAGER-IP:PORT
   ```
   For example to create a Swarm on the `manager1` machine:

    ```
    $ docker swarm init --listen-addr 192.168.99.100:2377

    Initializing a new swarm.
    ```

    Docker Swarm creates a Swarm and initializes a manager node.

    The `--listen-addr` flag configures the manager node to listen on port `2377`.

3. You can run the `docker info` command to view the current state of the Swarm:

    ```
    $ docker swarm info

    Containers: 2
     Running: 0
     Paused: 0
     Stopped: 2
    ...snip...
    Swarm:
     NodeID: 0biocneuj4h2k
     IsManager: YES
     Managers: 1
     Nodes: 1
    ...snip...
    ```

4. To view information about nodes, run the `docker node ls` command:

    ```
    $ docker node ls

    ID              NAME      STATUS  AVAILABILITY/MEMBERSHIP  MANAGER STATUS  LEADER
  0biocneuj4h2 *  manager1  READY   ACTIVE                   REACHABLE       Yes

    ```

    The `*` next to the node id, indicates that you're currently connected on this node.

    Docker Swarm automatically names the node for the machine host name.

    The tutorial covers other columns in later steps.

In the next section of the tutorial, we'll [add two more nodes](add-nodes.md) to the cluster.


<p style="margin-bottom:300px">&nbsp;</p>

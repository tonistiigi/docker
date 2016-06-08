<!--[metadata]>
+++
title = "Add nodes to the Swarm"
description = "Add nodes to the Swarm"
keywords = ["tutorial, cluster management, swarm"]
[menu.main]
identifier="add-nodes"
parent="swarm-tutorial"
weight=13
+++
<![end-metadata]-->

## Add nodes to the Swarm
Once you've [created a Swarm](create-swarm.md) with a manager node, you can add worker nodes.

1. Open a terminal and ssh into the machine where you want to run a worker node. This tutorial uses the name `worker1`.

2. Run the `docker swarm join` command to create a worker node joined to the existing Swarm. Pass the IP address of the manager node and the port where the manager listens:
    ```
    $ docker swarm join MANAGER-IP:PORT
    ```
    For example, to join the Swarm on `manager1` node for the tutorial:
    ```
    $ docker swarm join 192.168.99.100:2377

    This node is attempting to join a Swarm.
    ```

3. Open a terminal and ssh into the machine where the manager node runs and run the `docker node ls` command to see the new worker node:

    ```bashtext
    $ docker node ls

    ID              NAME      STATUS  AVAILABILITY/MEMBERSHIP  MANAGER STATUS  LEADER
    0biocneuj4h2 *  manager1  READY   ACTIVE                   REACHABLE       Yes
    0e1viboabog2    worker1   READY   ACTIVE
    ```

    The `MANAGER` column identifies the manager nodes in the Swarm. The empty status in this column for `worker1` identifies it as a worker node.

    Swarm management commands like `docker node ls` only work on manager nodes.

4. Repeat these steps for your your second worker node: `worker2`. When you run `docker node ls` from your manager node, you can see all three nodes--one manager and two workers:

    ```bashtext
    $ docker node ls

    ID              NAME      STATUS  AVAILABILITY/MEMBERSHIP  MANAGER STATUS  LEADER
    0biocneuj4h2 *  manager1  READY   ACTIVE                   REACHABLE       Yes
    0e1viboabog2    worker1   READY   ACTIVE
    24kgbigepmuq    worker2   READY   ACTIVE
    ```

Now your Swarm consists of a manager and two worker nodes. In the next step of the tutorial, you [deploy a service](deploy-service.md) to the Swarm.

<p style="margin-bottom:300px">&nbsp;</p>

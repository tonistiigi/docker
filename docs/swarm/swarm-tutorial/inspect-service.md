<!--[metadata]>
+++
title = "Inspect the service"
description = "Inspect the application"
keywords = ["tutorial, cluster management, swarm"]
[menu.main]
identifier="inspect-application"
parent="swarm-tutorial"
weight=17
+++
<![end-metadata]-->

## Inspect a service on the Swarm
When you have [deployed a service](deploy-service.md) to your Swarm, you can use the Docker CLI to see details about the service running in the Swarm.

1. If you haven't already, open a terminal and ssh into the machine where you run your manager node. For example, the tutorial uses a machine named `manager1`.

2. You can view details about the service with the `docker service inspect` command. Use the `--pretty` flag to display the output in an easily readable format.
    ```
    $ docker service inspect --pretty SERVICE-ID
    ```
    To see the details on the `helloworld` service:
    ```
    $ docker service inspect --pretty helloworld

    ID:		2zs4helqu64f3k3iuwywbk49w
    Name:		helloworld
    Mode:		REPLICATED
     Scale:	1
    Placement:
     Strategy:	SPREAD
    UpateConfig:
     Parallelism:	1
    ContainerSpec:
     Image:		alpine
     Command:	ping docker.com
    ```
    
3. To return the service details in json format, you can run the following command:
    ```
    $ docker service inspect SERVICE-ID
     ```
    For example:
    ```
    $ docker service inspect helloworld
    [
    {
        "ID": "2zs4helqu64f3k3iuwywbk49w",
        "Version": {
            "Index": 16264
        },
        "CreatedAt": "2016-06-06T17:41:11.509146705Z",
        "UpdatedAt": "2016-06-06T17:41:11.510426385Z",
        "Spec": {
            "Name": "helloworld",
            "ContainerSpec": {
                "Image": "alpine",
                "Command": [
                    "ping",
                    "docker.com"
                ],
                "Resources": {
                    "Limits": {},
                    "Reservations": {}
                }
            },
            "Mode": {
                "Replicated": {
                    "Instances": 1
                }
            },
            "RestartPolicy": {},
            "Placement": {},
            "UpdateConfig": {
                "Parallelism": 1
            },
            "EndpointSpec": {}
        },
        "Endpoint": {
            "Spec": {}
        }
    }
    ]
    ```

4. The `docker ps` command returns the service containers:
    ```bashtext
    $docker ps

    CONTAINER ID        IMAGE               COMMAND             CREATED             STATUS              PORTS               NAMES
    a0b6c02868ca        alpine:latest       "ping docker.com"   12 minutes ago      Up 12 minutes                           helloworld.1.1n6wif51j0w840udalgw6hphg
    ```

5. You can run the `docker service tasks` command to see which nodes are running the service:
    ```
    $ docker service tasks SERVICE-ID
    ```
    For the `helloworld` service in the example:
    ```
    $ docker service tasks helloworld

    ID                         NAME          SERVICE     IMAGE   DESIRED STATE  LAST STATE          NODE
    1n6wif51j0w840udalgw6hphg  helloworld.1  helloworld  alpine  RUNNING        RUNNING 19 minutes  manager1
    ```
    In this case, the one instance of the `helloworld` service is running on the `manager1` node. Manager nodes in a Swarm can execute tasks just like worker nodes.

    Swarm also shows you the `DESIRED STATE` and `LAST STATE` of the service task so you can see if tasks are running according to the service definition.

  Next, you can [change the scale](scale-service.md) for the service running in the Swarm.
<p style="margin-bottom:300px">&nbsp;</p>

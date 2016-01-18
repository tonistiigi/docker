package libcontainerd

import (
	"fmt"
	"io"
	"log"
	"net"
	"os/exec"
	"sync"
	"time"

	"github.com/Sirupsen/logrus"
	containerd "github.com/docker/containerd/api/grpc/types"
	"github.com/docker/docker/pkg/term"
	"github.com/opencontainers/runc/libcontainer"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
)

type StateInfo struct {
	State     string
	Pid       uint32
	ExitCode  uint32
	OOMKilled bool
}

type Backend interface {
	StateChanged(id string, state StateInfo) error
	ProcessExited(id, processId string, exitCode uint32) error
	PrepareContainerIOStreams(id string) (*IO, error)
} // maybe state change instead

// how to do the restore of containers? Restore(id) err , on err kill the old one. how to get the pid?

type Client interface {
	Create(id, bundlePath string) error
	Signal(id string, sig int) error
	AddProcess(id, processId string, req AddProcessRequest) error
	Resize(id string, width, height int) error
	ResizeProcess(id, processId string, width, height int) error
	Pause(id string) error
	Resume(id string) error
	Restore(id string, terminal bool) error
	Stats(id string) (*containerd.Stats, error)
}

func New(b Backend, addr string, createIfMissing bool) (Client, error) {
	if createIfMissing && addr == "" {
		logrus.Debugf("Starting containerd")
		cmd := exec.Command("containerd", "--debug", "true")
		cmd.Start()
		time.Sleep(time.Second) // FIXME: better detection
		addr = "/run/containerd/containerd.sock"
	}

	dialOpts := []grpc.DialOption{grpc.WithInsecure()}
	dialOpts = append(dialOpts,
		grpc.WithDialer(func(addr string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("unix", addr, timeout)
		}),
	)

	conn, err := grpc.Dial(addr, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("Error connecting to containerd: %v", err)
	}
	go func() {
		state := grpc.Idle
		for {
			s, err := conn.WaitForStateChange(context.Background(), state)
			logrus.Println("connstate", s, err)
			if err == nil {
				state = s
			}
		}
	}()

	// FIXME conn cleanup
	apiClient := containerd.NewAPIClient(conn)

	c := &client{backend: b, apiClient: apiClient, containers: make(map[string]*process)}

	if err := c.startMonitor(); err != nil {
		return nil, err
	}

	return c, nil
}

type process struct {
	sync.RWMutex
	stateMonitor
	pid      uint32
	console  libcontainer.Console
	bundle   string
	children map[string]*process
}

type client struct {
	sync.RWMutex
	backend    Backend
	apiClient  containerd.APIClient
	containers map[string]*process
}

func (c *client) startMonitor() error {
	events, err := c.apiClient.Events(context.Background(), &containerd.EventsRequest{})
	if err != nil {
		return err
	}
	go func() {
		for {
			e, err := events.Recv()
			if err != nil {
				logrus.Error(err) // FIXME: handle
				go c.startMonitor()
				return
			}
			logrus.Debugf("event: %+v ", e)

			if e.Type == "updateContainer" {
				state := ""
				// this should be already in event and combined with other handling
				resp, err := c.apiClient.State(context.Background(), &containerd.StateRequest{})
				if err != nil {
					logrus.Debugf("error getting containers state: %s", e.Id)
					continue
				}
				for _, c := range resp.Containers {
					if c.Id == e.Id {
						state = c.Status
						break
					}
				}
				log.Println("l")
				if state != "" {
					e.Type = state
				}
			}

			switch e.Type {
			case "execExit":
				container, err := c.getContainer(e.Id)
				if err != nil {
					logrus.Errorf("no state for container: %q", err)
					continue
				}
				container.Lock()
				for id, p := range container.children {
					if p.pid == e.Pid {
						err := c.backend.ProcessExited(e.Id, id, e.Status)
						if err != nil {
							logrus.Errorf("unhandled process exit for %s: %q", e.Id, err)
						}
					}
				}
				container.Unlock()
			case "exit", "paused", "running":
				container, err := c.getContainer(e.Id)
				if err != nil {
					logrus.Errorf("no state for container: %q", err)
					continue
				}
				container.Lock()
				container.handleMessage(c.backend, e)

				if e.Type == "paused" || e.Type == "running" {
					container.stateMonitor.handle(e.Type)
				}
				container.Unlock()
			default:
				logrus.Debugf("event unhandled: %+v", e)
			}
		}
	}()
	return nil
}

func (p *process) handleMessage(b Backend, e *containerd.Event) {
	log.Println("handleMessage", e.Type)

	err := b.StateChanged(e.Id, StateInfo{
		State:    e.Type,
		ExitCode: e.Status,
		Pid:      e.Pid,
	})
	if err != nil {
		logrus.Errorf("unhandled exit for %s: %q", e.Id, err)
	}
}

func (c *client) Signal(id string, sig int) error {
	container, err := c.getContainer(id)
	if err != nil {
		return err
	}
	if container.pid == 0 {
		return fmt.Errorf("No active process for container %s", id)
	}
	_, err = c.apiClient.Signal(context.Background(), &containerd.SignalRequest{
		Id:     id,
		Pid:    uint32(container.pid),
		Signal: uint32(sig),
	})
	return err
}
func (c *client) Create(id, bundlePath string) (err error) {
	c.Lock()
	if _, ok := c.containers[id]; ok {
		return fmt.Errorf("Container %s is aleady active")
	}
	c.containers[id] = nil
	c.Unlock()

	defer func() {
		if err != nil {
			c.Lock()
			delete(c.containers, id)
			c.Unlock()
		}
	}()

	streams, err := GetStreams(bundlePath, id)
	if err != nil {
		return err
	}

	r := &containerd.CreateContainerRequest{
		Id:         id,
		BundlePath: bundlePath,
		Stdin:      streams.FifoPath(0),
		Stdout:     streams.FifoPath(1),
		Stderr:     streams.FifoPath(2),
	}

	container := &process{
		// console: console,
		bundle: bundlePath,
	}

	io, err := c.backend.PrepareContainerIOStreams(id)
	if err != nil {
		return err
	}

	if err := streams.Attach(io.Stdin, io.Stdout, io.Stderr); err != nil {
		return err
	}

	resp, err := c.apiClient.CreateContainer(context.Background(), r)
	if err != nil {
		return err
	}
	container.pid = resp.Pid

	c.Lock()
	container.Lock()
	defer container.Unlock()
	c.containers[id] = container
	c.Unlock()

	return c.backend.StateChanged(id, StateInfo{
		State: "started",
		Pid:   resp.Pid,
	})
}

func (c *client) restore(id, bundlePath string, pid uint32) (err error) {
	c.Lock()
	if _, ok := c.containers[id]; ok {
		return fmt.Errorf("Container %s is aleady active")
	}
	c.containers[id] = nil
	c.Unlock()

	defer func() {
		if err != nil {
			c.Lock()
			delete(c.containers, id)
			c.Unlock()
		}
	}()

	streams, err := GetStreams(bundlePath, id)
	if err != nil {
		return err
	}

	io, err := c.backend.PrepareContainerIOStreams(id)
	if err != nil {
		return err
	}
	logrus.Debugf("attaching streams")
	if err := streams.Attach(io.Stdin, io.Stdout, io.Stderr); err != nil {
		return err
	}

	container := &process{
		// console: console,
		bundle: bundlePath,
		pid:    pid,
	}

	c.Lock()
	container.Lock()
	defer container.Unlock()
	c.containers[id] = container
	c.Unlock()

	return c.backend.StateChanged(id, StateInfo{
		State: "restored",
		Pid:   pid,
	})
	return nil
}

func (c *client) getContainer(id string) (*process, error) {
	c.RLock()
	container, ok := c.containers[id]
	c.RUnlock()
	if !ok || container == nil {
		return nil, fmt.Errorf("Invalid container: %s", id)
	}
	return container, nil
}

func (c *client) Resize(id string, width, height int) error {
	container, err := c.getContainer(id)
	if err != nil {
		return err
	}
	if container.console == nil {
		return fmt.Errorf("No active terminal for container %s", id)
	}
	return term.SetWinsize(container.console.Fd(), &term.Winsize{
		Height: uint16(height),
		Width:  uint16(width),
	})
}

func (c *client) ResizeProcess(id, processId string, width, height int) error {
	container, err := c.getContainer(id)
	if err != nil {
		return err
	}
	container.RLock()
	process, ok := container.children[processId]
	container.RUnlock()
	if !ok {
		return fmt.Errorf("Invalid process: %s", id)
	}
	if process.console == nil {
		return fmt.Errorf("No active terminal for process %s", processId)
	}
	return term.SetWinsize(process.console.Fd(), &term.Winsize{
		Height: uint16(height),
		Width:  uint16(width),
	})
}

func (c *client) AddProcess(id, processId string, req AddProcessRequest) error {
	// fixme: locks
	cont, ok := c.containers[id]
	if !ok {
		return fmt.Errorf("invalid container: %s", id)
	}

	streams, err := GetStreams(cont.bundle, processId)
	if err != nil {
		return err
	}

	r := &containerd.AddProcessRequest{
		Args:     req.Args,
		Cwd:      req.Cwd,
		Terminal: req.Terminal,
		Id:       id,
		Env:      req.Env, // FIXME
		User:     req.User,
		Stdin:    streams.FifoPath(0),
		Stdout:   streams.FifoPath(1),
		Stderr:   streams.FifoPath(2),
	}

	if err := streams.Attach(req.Stdin, req.Stdout, req.Stderr); err != nil {
		return err
	}

	container := &process{
	// console: console,
	}

	c.Lock() // todo: maybe lock early by ID
	defer c.Unlock()

	resp, err := c.apiClient.AddProcess(context.Background(), r)
	if err != nil {
		return err
	}

	container.pid = resp.Pid
	if c.containers[id].children == nil {
		c.containers[id].children = make(map[string]*process)
	}

	c.containers[id].children[processId] = container

	return nil
}
func (c *client) Pause(id string) error {
	return c.setState(id, "paused")
}

func (c *client) setState(id, state string) error {
	container, err := c.getContainer(id)
	if err != nil {
		return err
	}
	if container.pid == 0 {
		return fmt.Errorf("No active process for container %s", id)
	}
	container.Lock()
	ch := make(chan struct{})
	_, err = c.apiClient.UpdateContainer(context.Background(), &containerd.UpdateContainerRequest{
		Id:     id,
		Status: state,
	})
	if err != nil {
		return err
	}
	container.stateMonitor.append(state, ch)
	container.Unlock()
	<-ch
	return nil
}

func (c *client) Resume(id string) error {
	return c.setState(id, "running")
}
func (c *client) Stats(id string) (*containerd.Stats, error) {
	req := &containerd.PullStatsRequest{
		Ids: []string{id},
	}
	resp, err := c.apiClient.PullStats(context.Background(), req)
	if err != nil {
		return nil, err
	}
	stats, ok := resp.Stats[id]
	if !ok {
		return nil, fmt.Errorf("invalid stats response")
	}
	return stats, nil
}
func (c *client) Info() {
}
func (c *client) Restore(id string, console bool) error {
	// TODO: optimize this into per container call
	resp, err := c.apiClient.State(context.Background(), &containerd.StateRequest{})
	if err != nil {
		return fmt.Errorf("error getting containers state: %s", id)
	}
	for _, cont := range resp.Containers {
		if cont.Id == id {
			state := cont.Status
			logrus.Debugf("container %s state %s", cont.Id, state)
			if state == "running" {
				var pid uint32
				if len(cont.Processes) != 0 {
					pid = cont.Processes[0].Pid
				}
				return c.restore(cont.Id, cont.BundlePath, pid)
			}
			return c.backend.StateChanged(cont.Id, StateInfo{
				State: state,
			})
		}
	}
	logrus.Debugf("Container %s unknown")
	return nil
}

type IO struct {
	Stdin    io.ReadCloser
	Stdout   io.Writer
	Stderr   io.Writer
	Terminal bool
}

// type CreateContainerRequest struct {
// 	BundlePath string
// }

type AddProcessRequest struct {
	IO
	Args []string
	Cwd  string
	Env  []string
	User *containerd.User
}

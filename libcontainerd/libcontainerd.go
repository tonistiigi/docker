package libcontainerd

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
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
} // maybe state change instead

// how to do the restore of containers? Restore(id) err , on err kill the old one. how to get the pid?

type Client interface {
	Create(id string, req CreateContainerRequest) error
	Signal(id string, sig int) error
	AddProcess(id, processId string, req AddProcessRequest) error
	Resize(id string, width, height int) error
	ResizeProcess(id, processId string, width, height int) error
	Pause(id string) error
	Resume(id string) error
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
	pid      uint32
	console  libcontainer.Console
	bundle   string
	children map[string]*process
	cbs      map[string][]chan struct{}
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
					if len(container.cbs[e.Type]) > 0 {
						close(container.cbs[e.Type][0])
						container.cbs[e.Type] = container.cbs[e.Type][1:]
					}
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
func (c *client) Create(id string, req CreateContainerRequest) (err error) {
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

	r := &containerd.CreateContainerRequest{
		Id:         id,
		BundlePath: req.BundlePath,
	}

	stdin, console, err := req.Streams.attach(filepath.Join(r.BundlePath, "main"), &r.Stdin, &r.Stdout, &r.Stderr, &r.Console)
	if err != nil {
		return err
	}
	container := &process{
		console: console,
		bundle:  req.BundlePath,
		cbs:     map[string][]chan struct{}{"paused": nil, "running": nil},
	}

	resp, err := c.apiClient.CreateContainer(context.Background(), r)
	if err != nil {
		return err
	}
	if stdin != nil && req.Stdin != nil {
		go func() {
			io.Copy(stdin, req.Stdin)
			stdin.Close() // support leaving open
		}()
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

func (s *Streams) attach(basePath string, stdin, stdout, stderr, console *string) (io.WriteCloser, libcontainer.Console, error) {
	if s.Terminal {
		c, err := libcontainer.NewConsole(os.Getuid(), os.Getgid())
		if err != nil {
			return nil, nil, err
		}
		*console = c.Path()

		go func() {
			io.Copy(s.Stdout, c)
		}()
		return c, c, nil
	}
	var stdinStream io.WriteCloser
	for _, p := range []struct {
		path string
		flag int
		done func(f *os.File)
	}{
		{
			path: basePath + "-stdin",
			flag: syscall.O_RDWR,
			done: func(f *os.File) {
				*stdin = f.Name()
				stdinStream = f
			},
		},
		{
			path: basePath + "-stdout",
			flag: syscall.O_RDWR,
			done: func(f *os.File) {
				*stdout = f.Name()
				go func() {
					io.Copy(s.Stdout, f)
					f.Close()
				}()
			},
		},
		{
			path: basePath + "-stderr",
			flag: syscall.O_RDWR,
			done: func(f *os.File) {
				*stderr = f.Name()
				go func() {
					io.Copy(s.Stderr, f)
					f.Close()
				}()
			},
		},
	} {
		if err := syscall.Mkfifo(p.path, 0755); err != nil {
			return nil, nil, fmt.Errorf("mkfifo: %s %v", p.path, err)
		}
		f, err := os.OpenFile(p.path, p.flag, 0)
		if err != nil {
			return nil, nil, fmt.Errorf("open: %s %v", p.path, err)
		}
		p.done(f)
	}
	return stdinStream, nil, nil
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
	r := &containerd.AddProcessRequest{
		Args:     req.Args,
		Cwd:      req.Cwd,
		Terminal: req.Terminal,
		Id:       id,
		Env:      req.Env, // FIXME
		User:     req.User,
	}
	cont, ok := c.containers[id]
	if !ok {
		return fmt.Errorf("invalid container: %s", id)
	}

	stdin, console, err := req.Streams.attach(filepath.Join(cont.bundle, processId), &r.Stdin, &r.Stdout, &r.Stderr, &r.Console)
	if err != nil {
		return err
	}
	container := &process{
		console: console,
	}

	c.Lock() // todo: maybe lock early by ID
	defer c.Unlock()

	resp, err := c.apiClient.AddProcess(context.Background(), r)
	if err != nil {
		return err
	}

	if stdin != nil && req.Stdin != nil {
		go func() {
			io.Copy(stdin, req.Stdin)
			stdin.Close() // support leaving open
		}()
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
	container.cbs[state] = append(container.cbs[state], ch)
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
func (c *client) Restore(id string) error {
	return fmt.Errorf("not implemented")
}

type Streams struct {
	Stdin    io.ReadCloser
	Stdout   io.Writer
	Stderr   io.Writer
	Terminal bool
}

type CreateContainerRequest struct {
	Streams
	BundlePath string
}

type AddProcessRequest struct {
	Streams
	Args []string
	Cwd  string
	Env  []string
	User *containerd.User
}

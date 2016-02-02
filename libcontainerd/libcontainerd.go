package libcontainerd

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/Sirupsen/logrus"
	containerd "github.com/docker/containerd/api/grpc/types"
	"github.com/docker/docker/pkg/ioutils"
	sysinfo "github.com/docker/docker/pkg/system"
	"github.com/docker/docker/utils"
	"github.com/opencontainers/runc/libcontainer"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
)

const (
	maxConnectionRetryCount = 3
	connectionRetryDelay    = 3 * time.Second
)

var fdnames = [3]string{"stdin", "stdout", "stderr"}

// StateInfo contains description about the new state container has entered.
type StateInfo struct {
	State     string
	Pid       uint32
	ExitCode  uint32
	OOMKilled bool
}

// Backend defines callbacks that the client of the library needs to implement.
type Backend interface {
	StateChanged(id string, state StateInfo) error
	ProcessExited(id, processID string, exitCode uint32) error
	AttachStreams(id string, io IOPipe) error
}

// Client privides access to containerd features.
type Client interface {
	Create(id, bundlePath string) error
	Signal(id string, sig int) error
	AddProcess(id, processID string, req AddProcessRequest) error
	Resize(id, processID string, width, height int) error
	Pause(id string) error
	Resume(id string) error
	Restore(id string) error
	Stats(id string) (*containerd.Stats, error)
}

type IOPipe struct {
	Stdin    io.WriteCloser
	Stdout   io.Reader
	Stderr   io.Reader
	Terminal bool
}

// AddProcessRequest describes a new process created inside existing container.
type AddProcessRequest struct {
	Terminal bool
	Args     []string
	Cwd      string
	Env      []string
	User     *containerd.User
}

type client struct {
	sync.RWMutex
	backend    Backend
	apiClient  containerd.APIClient
	containers map[string]*process
}

// process keeps the state for both main container process and exec process.
type process struct {
	sync.RWMutex
	stateMonitor

	oom      bool
	console  libcontainer.Console
	pid      uint32
	bundle   string
	children map[string]*process
}

func startDaemon(root, addr string) (int, error) {
	if err := sysinfo.MkdirAll(root, 0700); err != nil {
		return -1, err
	}

	path := filepath.Join(root, "containerd.pid")

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	defer f.Close()
	if err != nil {
		return -1, err
	}

	// File exist, check if the daemon is alive
	b := make([]byte, 8)
	n, err := f.Read(b)
	if err != nil && err != io.EOF {
		return -1, err
	}

	if n > 0 {
		pid, err := strconv.ParseUint(string(b[:n]), 10, 64)
		if err != nil {
			return -1, err
		}
		if utils.IsProcessAlive(int(pid)) {
			logrus.Infof("Previous instance of containerd still alive (%d)", pid)
			return int(pid), nil
		}
	}

	// rewind the file
	_, err = f.Seek(0, os.SEEK_SET)
	if err != nil {
		return -1, err
	}

	// Truncate it
	err = f.Truncate(0)
	if err != nil {
		return -1, err
	}

	// Start a new instance
	cmd := exec.Command("containerd", "-l", addr, "--debug", "true")
	// TODO(mlaventure): get optional flag for debug mode
	// TODO: store logs?
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = cmd.Start()
	if err != nil {
		return -1, err
	}

	logrus.Infof("New containerd pid: %d\n", cmd.Process.Pid)

	if _, err := f.WriteString(fmt.Sprintf("%d", cmd.Process.Pid)); err != nil {
		utils.KillProcess(cmd.Process.Pid)
		return -1, err
	}

	go func() {
		// Reap our child when needed
		cmd.Wait()
	}()

	return cmd.Process.Pid, nil
}

// New creates a fresh instance of libcontainerd client.
func New(b Backend, execRoot, addr string, createIfMissing bool) (Client, error) {
	var daemonPid = -1

	if createIfMissing && addr == "" {
		var err error
		addr = filepath.Join(execRoot, "containerd.sock")
		if daemonPid, err = startDaemon(execRoot, addr); err != nil {
			return nil, err
		}
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
		var transientFailureCount = 0
		state := grpc.Idle
		for {
			s, err := conn.WaitForStateChange(context.Background(), state)
			logrus.Println("connstate", s, err)
			if err == nil {
				state = s
			}

			if daemonPid != -1 {
				switch state {
				case grpc.TransientFailure:
					// Reset state to be notified of next failure
					transientFailureCount++
					if transientFailureCount >= maxConnectionRetryCount {
						transientFailureCount = 0
						if utils.IsProcessAlive(daemonPid) {
							utils.KillProcess(daemonPid)
						}
						daemonPid, _ = startDaemon(execRoot, addr)
					} else {
						state = grpc.Idle
						time.Sleep(connectionRetryDelay)
					}
				case grpc.Shutdown:
					// Well, We asked for it to stop, just return
					return
				}
			}
		}
	}()

	apiClient := containerd.NewAPIClient(conn)

	c := &client{
		backend:    b,
		apiClient:  apiClient,
		containers: make(map[string]*process),
	}

	if err := c.startMonitor(); err != nil {
		return nil, err
	}

	return c, nil
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
				logrus.Error(err)
				go c.startMonitor()
				return
			}

			log.Println("event:", e)

			// TODO(mlaventure): use github.com/docker/containerd/supervisor.UpdateContainerEventType for matching events type
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
				if state != "" {
					e.Type = state
				}
			}

			switch e.Type {
			case "oom":
				container, err := c.getContainer(e.Id)
				if err != nil {
					logrus.Errorf("no state for container: %q", err)
					continue
				}
				container.Lock()
				container.oom = true
				container.Unlock()

			case "exit", "paused", "running":
				container, err := c.getContainer(e.Id)
				if err != nil {
					logrus.Errorf("no state for container: %q", err)
					continue
				}

				if e.Type == "exit" && e.Pid != "init" {
					container.Lock()
					err := c.backend.ProcessExited(e.Id, e.Pid, e.Status)
					if err != nil {
						logrus.Errorf("unhandled process exit for %s: %q", e.Id, err)
					}
					container.Unlock()
					continue
				}

				// Remove container from list if we have exited
				// We need to do so here in case the Message Handler decides to restart it.
				c.Lock()
				if e.Type == "exit" {
					delete(c.containers, e.Id)
				}
				c.Unlock()

				container.Lock()

				if err := c.backend.StateChanged(e.Id, StateInfo{
					State:     e.Type,
					ExitCode:  e.Status,
					OOMKilled: e.Type == "exit" && container.oom,
				}); err != nil {
					logrus.Errorf("unhandled state change for %s: %q", e.Id, err)
				}

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

func (c *client) Signal(id string, sig int) error {
	_, err := c.getContainer(id)
	if err != nil {
		return err
	}
	_, err = c.apiClient.Signal(context.Background(), &containerd.SignalRequest{
		Id:     id,
		Pid:    "init", //uint32(container.pid),
		Signal: uint32(sig),
	})
	return err
}

func (c *client) Create(id, bundlePath string) (err error) {
	c.Lock()
	if _, ok := c.containers[id]; ok {
		return fmt.Errorf("Container %s is aleady active", id)
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

	iopipe, err := c.openFifos(bundlePath, id, "init")
	if err != nil {
		return err
	}

	r := &containerd.CreateContainerRequest{
		Id:         id,
		BundlePath: bundlePath,
		Stdin:      fifoname(bundlePath, "init", syscall.Stdin),
		Stdout:     fifoname(bundlePath, "init", syscall.Stdout),
		Stderr:     fifoname(bundlePath, "init", syscall.Stderr),
	}

	container := &process{
		bundle: bundlePath,
	}

	_, err = c.apiClient.CreateContainer(context.Background(), r)
	if err != nil {
		return err
	}

	// FIXME: is there a race for closing stdin before container starts
	if err := c.backend.AttachStreams(id, *iopipe); err != nil {
		return err
	}
	// container.pid = resp.Pid

	c.Lock()
	container.Lock()
	defer container.Unlock()
	c.containers[id] = container
	c.Unlock()

	return c.backend.StateChanged(id, StateInfo{
		State: "started",
		Pid:   0, // FIXME:
	})
}

func (c *client) restore(id, bundlePath string, pid uint32) (err error) {
	c.Lock()
	if _, ok := c.containers[id]; ok {
		return fmt.Errorf("Container %s is aleady active", id)
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

	iopipe, err := c.openFifos(bundlePath, id, "init")
	if err != nil {
		return err
	}

	if err := c.backend.AttachStreams(id, *iopipe); err != nil {
		return err
	}

	container := &process{
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
}

func (c *client) Resize(id, processID string, width, height int) error {
	_, err := c.apiClient.UpdateProcess(context.Background(), &containerd.UpdateProcessRequest{
		Id:     id,
		Pid:    processID,
		Width:  uint32(width),
		Height: uint32(height),
	})
	return err
}

func (c *client) AddProcess(id, processID string, req AddProcessRequest) error {
	// fixme: locks
	cont, ok := c.containers[id]
	if !ok {
		return fmt.Errorf("invalid container: %s", id)
	}

	r := &containerd.AddProcessRequest{
		Args:     req.Args,
		Cwd:      req.Cwd,
		Terminal: req.Terminal,
		Id:       id,
		Env:      req.Env,
		User:     req.User,
		Pid:      processID,
		Stdin:    fifoname(cont.bundle, processID, syscall.Stdin),
		Stdout:   fifoname(cont.bundle, processID, syscall.Stdout),
		Stderr:   fifoname(cont.bundle, processID, syscall.Stderr),
	}

	container := &process{}

	c.Lock() // todo: maybe lock early by ID
	defer c.Unlock()

	iopipe, err := c.openFifos(cont.bundle, id, processID)
	if err != nil {
		return err
	}

	_, err = c.apiClient.AddProcess(context.Background(), r)
	if err != nil {
		return err
	}

	if err := c.backend.AttachStreams(processID, *iopipe); err != nil {
		return err
	}

	if c.containers[id].children == nil {
		c.containers[id].children = make(map[string]*process)
	}

	c.containers[id].children[processID] = container

	return nil
}

func (c *client) openFifos(base, id, pid string) (*IOPipe, error) {
	for i := 0; i < 3; i++ {
		p := fifoname(base, pid, i)
		if err := syscall.Mkfifo(p, 0700); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("mkfifo: %s %v", p, err)
		}
	}

	io := &IOPipe{}
	// FIXME: O_RDWR? open one-sided in goroutines?
	stdinf, err := os.OpenFile(fifoname(base, pid, syscall.Stdin), syscall.O_RDWR, 0)
	if err != nil {
		return nil, err
	}

	stdoutf, err := os.OpenFile(fifoname(base, pid, syscall.Stdout), syscall.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	io.Stdout = stdoutf

	stderrf, err := os.OpenFile(fifoname(base, pid, syscall.Stderr), syscall.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	io.Stderr = stderrf

	io.Stdin = ioutils.NewWriteCloserWrapper(stdinf, func() error {
		stdinf.Close()
		_, err := c.apiClient.UpdateProcess(context.Background(), &containerd.UpdateProcessRequest{
			Id:         id,
			Pid:        pid,
			CloseStdin: true,
		})
		return err
	})

	return io, nil
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
		container.Unlock()
		return err
	}
	container.Unlock()

	container.stateMonitor.append(state, ch)
	container.Unlock()
	<-ch
	return nil
}

func (c *client) Resume(id string) error {
	return c.setState(id, "running")
}

func (c *client) Stats(id string) (*containerd.Stats, error) {
	// req := &containerd.PullStatsRequest{
	// 	Ids: []string{id},
	// }
	// resp, err := c.apiClient.PullStats(context.Background(), req)
	// if err != nil {
	// 	return nil, err
	// }
	// stats, ok := resp.Stats[id]
	// if !ok {
	// 	return nil, fmt.Errorf("invalid stats response")
	// }
	// return stats, nil
	return nil, nil
}

func (c *client) Restore(id string) error {
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
				// FIXME: getting the pid
				return c.restore(cont.Id, cont.BundlePath, 0)
			}
			return c.backend.StateChanged(cont.Id, StateInfo{
				State: state,
			})
		}
	}

	return c.backend.StateChanged(id, StateInfo{
		State: "exit",
	})
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

func fifoname(base, id string, i int) string {
	return filepath.Join(base, id+"-"+fdnames[i])
}

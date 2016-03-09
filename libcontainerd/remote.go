package libcontainerd

import (
	"fmt"
	"io"
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
	sysinfo "github.com/docker/docker/pkg/system"
	"github.com/docker/docker/utils"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
)

const (
	maxConnectionRetryCount   = 3
	connectionRetryDelay      = 3 * time.Second
	containerdShutdownTimeout = 15 * time.Second
	containerdBinary          = "containerd"
	containerdPidFilename     = "containerd.pid"
	containerdSockFilename    = "containerd.sock"
	eventTimestampFilename    = "event.ts"
)

// Remote defines accesspoint to containerd grpc API.
type Remote interface {
	// Client returns a new Client instance connected with given Backend.
	Client(Backend) (Client, error)
	// Cleanup stops containerd if it was started by libcontainerd.
	Cleanup()
}

// RemoteOption allows to configure paramters of remotes.
type RemoteOption interface {
	Apply(Remote) error
}

type remote struct {
	sync.RWMutex
	apiClient   containerd.APIClient
	daemonPid   int
	stateDir    string
	rpcAddr     string
	startDaemon bool
	debugLog    bool
	rpcConn     *grpc.ClientConn
	clients     []*client
	eventTsPath string
	pastEvents  map[string]*containerd.Event
}

// New creates a fresh instance of libcontainerd remote.
func New(stateDir string, options ...RemoteOption) (Remote, error) {
	r := &remote{
		stateDir:    stateDir,
		daemonPid:   -1,
		eventTsPath: filepath.Join(stateDir, eventTimestampFilename),
		pastEvents:  make(map[string]*containerd.Event),
	}
	for _, option := range options {
		if err := option.Apply(r); err != nil {
			return nil, err
		}
	}

	if err := sysinfo.MkdirAll(stateDir, 0700); err != nil {
		return nil, err
	}

	if r.rpcAddr == "" {
		r.rpcAddr = filepath.Join(stateDir, containerdSockFilename)
	}

	if r.startDaemon {
		if err := r.runContainerdDaemon(); err != nil {
			return nil, err
		}
	}

	dialOpts := append([]grpc.DialOption{grpc.WithInsecure()},
		grpc.WithDialer(func(addr string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("unix", addr, timeout)
		}),
	)
	conn, err := grpc.Dial(r.rpcAddr, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("error connecting to containerd: %v", err)
	}

	r.rpcConn = conn
	r.apiClient = containerd.NewAPIClient(conn)

	go r.handleConnectionChange()

	if err := r.startEventsMonitor(); err != nil {
		return nil, err
	}

	return r, nil
}

func (r *remote) handleConnectionChange() {
	var transientFailureCount = 0
	state := grpc.Idle
	for {
		s, err := r.rpcConn.WaitForStateChange(context.Background(), state)
		if err != nil {
			break
		}
		state = s
		logrus.Debugf("containerd connection state change: %v", s)

		if r.daemonPid != -1 {
			switch state {
			case grpc.TransientFailure:
				// Reset state to be notified of next failure
				transientFailureCount++
				if transientFailureCount >= maxConnectionRetryCount {
					transientFailureCount = 0
					if utils.IsProcessAlive(r.daemonPid) {
						utils.KillProcess(r.daemonPid)
					}
					if err := r.runContainerdDaemon(); err != nil { //FIXME: Handle error
						logrus.Errorf("error restarting containerd: %v", err)
					}
				} else {
					state = grpc.Idle
					time.Sleep(connectionRetryDelay)
				}
			case grpc.Shutdown:
				// Well, we asked for it to stop, just return
				return
			}
		}
	}
}

func (r *remote) Cleanup() {
	if r.daemonPid == -1 {
		return
	}
	r.rpcConn.Close()
	// Ask the daemon to quit
	syscall.Kill(r.daemonPid, syscall.SIGTERM)

	// Wait up to 15secs for it to stop
	for i := time.Duration(0); i < containerdShutdownTimeout; i += time.Second {
		if !utils.IsProcessAlive(r.daemonPid) {
			break
		}
		time.Sleep(time.Second)
	}

	if utils.IsProcessAlive(r.daemonPid) {
		logrus.Warnf("libcontainerd: containerd (%d) didn't stop within 15 secs, killing it\n", r.daemonPid)
		syscall.Kill(r.daemonPid, syscall.SIGKILL)
	}

	// cleanup some files
	os.Remove(filepath.Join(r.stateDir, containerdPidFilename))
	os.Remove(filepath.Join(r.stateDir, containerdSockFilename))
}

func (r *remote) Client(b Backend) (Client, error) {
	c := &client{
		backend:          b,
		remote:           r,
		containers:       make(map[string]*container),
		containerMutexes: make(map[string]*sync.Mutex),
	}

	r.Lock()
	r.clients = append(r.clients, c)
	r.Unlock()
	return c, nil
}

func (r *remote) updateEventTimestamp(t time.Time) {
	f, err := os.OpenFile(r.eventTsPath, syscall.O_CREAT|syscall.O_WRONLY|syscall.O_TRUNC, 0600)
	defer f.Close()
	if err != nil {
		logrus.Warnf("libcontainerd: failed to open event timestamp file: %v", err)
		return
	}

	b, err := t.MarshalText()
	if err != nil {
		logrus.Warnf("libcontainerd: failed to encode timestamp: %v", err)
		return
	}

	n, err := f.Write(b)
	if err != nil || n != len(b) {
		logrus.Warnf("libcontainerd: failed to update event timestamp file: %v", err)
		f.Truncate(0)
		return
	}

}

func (r *remote) getLastEventTimestamp() int64 {
	t := time.Now()

	fi, err := os.Stat(r.eventTsPath)
	if os.IsNotExist(err) {
		return t.Unix()
	}

	f, err := os.Open(r.eventTsPath)
	defer f.Close()
	if err != nil {
		logrus.Warn("libcontainerd: Unable to access last event ts: %v", err)
		return t.Unix()
	}

	b := make([]byte, fi.Size())
	n, err := f.Read(b)
	if err != nil || n != len(b) {
		logrus.Warn("libcontainerd: Unable to read last event ts: %v", err)
		return t.Unix()
	}

	t.UnmarshalText(b)

	return t.Unix()
}

func (r *remote) startEventsMonitor() error {
	// First, get past events
	er := &containerd.EventsRequest{
		Timestamp: uint64(r.getLastEventTimestamp()),
	}
	events, err := r.apiClient.Events(context.Background(), er)
	if err != nil {
		return err
	}
	go r.handleEventStream(events)
	return nil
}

func (r *remote) handleEventStream(events containerd.API_EventsClient) {
	live := false
	for {
		e, err := events.Recv()
		if err != nil {
			logrus.Errorf("failed to receive event from containerd: %v", err)
			go r.startEventsMonitor()
			return
		}

		if live == false {
			logrus.Debugf("received past containerd event: %#v", e)

			// Pause/Resume events should never happens after exit one
			switch e.Type {
			case StateExit:
				r.pastEvents[e.Id] = e
			case StatePause:
				r.pastEvents[e.Id] = e
			case StateResume:
				r.pastEvents[e.Id] = e
			case stateLive:
				live = true
				r.updateEventTimestamp(time.Unix(int64(e.Timestamp), 0))
			}
		} else {
			logrus.Debugf("received containerd event: %#v", e)

			var container *container
			var c *client
			r.RLock()
			for _, c = range r.clients {
				container, err = c.getContainer(e.Id)
				if err == nil {
					break
				}
			}
			r.RUnlock()
			if container == nil {
				logrus.Errorf("no state for container: %q", err)
				continue
			}

			if err := container.handleEvent(e); err != nil {
				logrus.Errorf("error processing state change for %s: %v", e.Id, err)
			}

			r.updateEventTimestamp(time.Unix(int64(e.Timestamp), 0))
		}
	}
}

func (r *remote) runContainerdDaemon() error {
	pidFilename := filepath.Join(r.stateDir, containerdPidFilename)
	f, err := os.OpenFile(pidFilename, os.O_RDWR|os.O_CREATE, 0600)
	defer f.Close()
	if err != nil {
		return err
	}

	// File exist, check if the daemon is alive
	b := make([]byte, 8)
	n, err := f.Read(b)
	if err != nil && err != io.EOF {
		return err
	}

	if n > 0 {
		pid, err := strconv.ParseUint(string(b[:n]), 10, 64)
		if err != nil {
			return err
		}
		if utils.IsProcessAlive(int(pid)) {
			logrus.Infof("previous instance of containerd still alive (%d)", pid)
			r.daemonPid = int(pid)
			return nil
		}
	}

	// rewind the file
	_, err = f.Seek(0, os.SEEK_SET)
	if err != nil {
		return err
	}

	// Truncate it
	err = f.Truncate(0)
	if err != nil {
		return err
	}

	// Start a new instance
	args := []string{"-l", r.rpcAddr}
	if r.debugLog {
		args = append(args, "--debug", "true")
	}
	cmd := exec.Command(containerdBinary, args...)
	// TODO: store logs?
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	logrus.Infof("New containerd process, pid: %d\n", cmd.Process.Pid)

	if _, err := f.WriteString(fmt.Sprintf("%d", cmd.Process.Pid)); err != nil {
		utils.KillProcess(cmd.Process.Pid)
		return err
	}

	go cmd.Wait() // Reap our child when needed
	r.daemonPid = cmd.Process.Pid
	return nil
}

// WithRemoteAddr sets the external containerd socket to connect to.
func WithRemoteAddr(addr string) RemoteOption {
	return rpcAddr(addr)
}

type rpcAddr string

func (a rpcAddr) Apply(r Remote) error {
	if remote, ok := r.(*remote); ok {
		remote.rpcAddr = string(a)
		return nil
	}
	return fmt.Errorf("WithRemoteAddr option not supported for this remote")
}

// WithStartDaemon defines if libcontainerd should also run containerd daemon.
func WithStartDaemon(start bool) RemoteOption {
	return startDaemon(start)
}

type startDaemon bool

func (s startDaemon) Apply(r Remote) error {
	if remote, ok := r.(*remote); ok {
		remote.startDaemon = bool(s)
		return nil
	}
	return fmt.Errorf("WithStartDaemon option not supported for this remote")
}

// WithDebugLog defines if containerd debug logs will be enabled for daemon.
func WithDebugLog(debug bool) RemoteOption {
	return debugLog(debug)
}

type debugLog bool

func (d debugLog) Apply(r Remote) error {
	if remote, ok := r.(*remote); ok {
		remote.debugLog = bool(d)
		return nil
	}
	return fmt.Errorf("WithDebugLog option not supported for this remote")
}

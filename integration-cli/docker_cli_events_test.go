package main

import (
	"bufio"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/kr/pty"
)

func TestEventsUntag(t *testing.T) {
	out, _, _ := cmd(t, "images", "-q")
	image := strings.Split(out, "\n")[0]
	cmd(t, "tag", image, "utest:tag1")
	cmd(t, "tag", image, "utest:tag2")
	cmd(t, "rmi", "utest:tag1")
	cmd(t, "rmi", "utest:tag2")
	// time.Sleep(200 * time.Millisecond)
	eventsCmd := exec.Command(dockerBinary, "events", "--since=1")
	out, _, _ = runCommandWithOutputAndTimeout(eventsCmd, 200*time.Millisecond)

	events := strings.Split(out, "\n")
	nEvents := len(events)
	// The last element after the split above will be an empty string, so we
	// get the two elements before the last, which are the untags we're
	// looking for.
	for _, v := range events[nEvents-3 : nEvents-1] {
		if !strings.Contains(v, "untag") {
			t.Fatalf("event should be untag, not %#v", v)
		}
	}
	logDone("events - untags are logged")
}

func TestEventsPause(t *testing.T) {
	out, _, _ := cmd(t, "images", "-q")
	image := strings.Split(out, "\n")[0]
	cmd(t, "run", "-d", "--name", "testeventpause", image, "sleep", "2")
	cmd(t, "pause", "testeventpause")
	cmd(t, "unpause", "testeventpause")
	eventsCmd := exec.Command(dockerBinary, "events", "--since=0", fmt.Sprintf("--until=%d", time.Now().Unix()))
	out, _, _ = runCommandWithOutput(eventsCmd)
	events := strings.Split(out, "\n")
	if len(events) <= 1 {
		t.Fatalf("Missing expected event")
	}

	pauseEvent := strings.Fields(events[len(events)-3])
	unpauseEvent := strings.Fields(events[len(events)-2])

	if pauseEvent[len(pauseEvent)-1] != "pause" {
		t.Fatalf("event should be pause, not %#v", pauseEvent)
	}
	if unpauseEvent[len(unpauseEvent)-1] != "unpause" {
		t.Fatalf("event should be pause, not %#v", unpauseEvent)
	}

	logDone("events - pause/unpause is logged")
}

func TestEventsContainerFailStartDie(t *testing.T) {
	out, _, _ := cmd(t, "images", "-q")
	image := strings.Split(out, "\n")[0]
	eventsCmd := exec.Command(dockerBinary, "run", "-d", "--name", "testeventdie", image, "blerg")
	_, _, err := runCommandWithOutput(eventsCmd)
	if err == nil {
		t.Fatalf("Container run with command blerg should have failed, but it did not")
	}

	eventsCmd = exec.Command(dockerBinary, "events", "--since=0", fmt.Sprintf("--until=%d", time.Now().Unix()))
	out, _, _ = runCommandWithOutput(eventsCmd)
	events := strings.Split(out, "\n")
	if len(events) <= 1 {
		t.Fatalf("Missing expected event")
	}

	startEvent := strings.Fields(events[len(events)-3])
	dieEvent := strings.Fields(events[len(events)-2])

	if startEvent[len(startEvent)-1] != "start" {
		t.Fatalf("event should be start, not %#v", startEvent)
	}
	if dieEvent[len(dieEvent)-1] != "die" {
		t.Fatalf("event should be die, not %#v", dieEvent)
	}

	logDone("events - container failed to start logs die")
}

func TestEventsLimit(t *testing.T) {
	for i := 0; i < 30; i++ {
		cmd(t, "run", "busybox", "echo", strconv.Itoa(i))
	}
	eventsCmd := exec.Command(dockerBinary, "events", "--since=0", fmt.Sprintf("--until=%d", time.Now().Unix()))
	out, _, _ := runCommandWithOutput(eventsCmd)
	events := strings.Split(out, "\n")
	nEvents := len(events) - 1
	if nEvents != 64 {
		t.Fatalf("events should be limited to 64, but received %d", nEvents)
	}
	logDone("events - limited to 64 entries")
}

func TestEventsContainerEvents(t *testing.T) {
	cmd(t, "run", "--rm", "busybox", "true")
	eventsCmd := exec.Command(dockerBinary, "events", "--since=0", fmt.Sprintf("--until=%d", time.Now().Unix()))
	out, exitCode, err := runCommandWithOutput(eventsCmd)
	if exitCode != 0 || err != nil {
		t.Fatalf("Failed to get events with exit code %d: %s", exitCode, err)
	}
	events := strings.Split(out, "\n")
	events = events[:len(events)-1]
	if len(events) < 4 {
		t.Fatalf("Missing expected event")
	}
	createEvent := strings.Fields(events[len(events)-4])
	startEvent := strings.Fields(events[len(events)-3])
	dieEvent := strings.Fields(events[len(events)-2])
	destroyEvent := strings.Fields(events[len(events)-1])
	if createEvent[len(createEvent)-1] != "create" {
		t.Fatalf("event should be create, not %#v", createEvent)
	}
	if startEvent[len(startEvent)-1] != "start" {
		t.Fatalf("event should be pause, not %#v", startEvent)
	}
	if dieEvent[len(dieEvent)-1] != "die" {
		t.Fatalf("event should be pause, not %#v", dieEvent)
	}
	if destroyEvent[len(destroyEvent)-1] != "destroy" {
		t.Fatalf("event should be pause, not %#v", destroyEvent)
	}

	logDone("events - container create, start, die, destroy is logged")
}

func TestEventsImageUntagDelete(t *testing.T) {
	name := "testimageevents"
	defer deleteImages(name)
	_, err := buildImage(name,
		`FROM scratch
		MAINTAINER "docker"`,
		true)
	if err != nil {
		t.Fatal(err)
	}
	if err := deleteImages(name); err != nil {
		t.Fatal(err)
	}
	eventsCmd := exec.Command(dockerBinary, "events", "--since=0", fmt.Sprintf("--until=%d", time.Now().Unix()))
	out, exitCode, err := runCommandWithOutput(eventsCmd)
	if exitCode != 0 || err != nil {
		t.Fatalf("Failed to get events with exit code %d: %s", exitCode, err)
	}
	events := strings.Split(out, "\n")
	t.Log(events)
	events = events[:len(events)-1]
	if len(events) < 2 {
		t.Fatalf("Missing expected event")
	}
	untagEvent := strings.Fields(events[len(events)-2])
	deleteEvent := strings.Fields(events[len(events)-1])
	if untagEvent[len(untagEvent)-1] != "untag" {
		t.Fatalf("untag should be untag, not %#v", untagEvent)
	}
	if deleteEvent[len(deleteEvent)-1] != "delete" {
		t.Fatalf("delete should be delete, not %#v", deleteEvent)
	}
	logDone("events - image untag, delete is logged")
}

// #5979
func TestEventsRedirectStdout(t *testing.T) {

	since := time.Now().Unix()

	cmd(t, "run", "busybox", "true")

	defer deleteAllContainers()

	file, err := ioutil.TempFile("", "")
	if err != nil {
		t.Fatalf("could not create temp file: %v", err)
	}
	defer os.Remove(file.Name())

	command := fmt.Sprintf("%s events --since=%d --until=%d > %s", dockerBinary, since, time.Now().Unix(), file.Name())
	_, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("Could not open pty: %v", err)
	}
	cmd := exec.Command("sh", "-c", command)
	cmd.Stdin = tty
	cmd.Stdout = tty
	cmd.Stderr = tty
	if err := cmd.Run(); err != nil {
		t.Fatalf("run err for command %q: %v", command, err)
	}

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		for _, c := range scanner.Text() {
			if unicode.IsControl(c) {
				t.Fatalf("found control character %v", []byte(string(c)))
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("Scan err for command %q: %v", command, err)
	}

	logDone("events - redirect stdout")
}

func TestEventsImagePull(t *testing.T) {
	since := time.Now().Unix()
	pullCmd := exec.Command(dockerBinary, "pull", "scratch")
	if out, _, err := runCommandWithOutput(pullCmd); err != nil {
		t.Fatal("pulling the scratch image from has failed: %s, %v", out, err)
	}

	eventsCmd := exec.Command(dockerBinary, "events",
		fmt.Sprintf("--since=%d", since),
		fmt.Sprintf("--until=%d", time.Now().Unix()))
	out, _, _ := runCommandWithOutput(eventsCmd)

	events := strings.Split(strings.TrimSpace(out), "\n")
	event := strings.TrimSpace(events[len(events)-1])

	if !strings.HasSuffix(event, "scratch:latest: pull") {
		t.Fatalf("Missing pull event - got:%q", event)
	}

	logDone("events - image pull is logged")
}

func TestEventsImageImport(t *testing.T) {
	if !cliIsLocal() {
		t.Skip("skipping: uses local server")
	}

	since := time.Now().Unix()

	server, err := fileServer(map[string]string{
		"/cirros.tar.gz": "/cirros.tar.gz",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	fileURL := fmt.Sprintf("%s/cirros.tar.gz", server.URL)
	importCmd := exec.Command(dockerBinary, "import", fileURL)
	out, _, err := runCommandWithOutput(importCmd)
	if err != nil {
		t.Errorf("import failed with errors: %v, output: %q", err, out)
	}

	eventsCmd := exec.Command(dockerBinary, "events",
		fmt.Sprintf("--since=%d", since),
		fmt.Sprintf("--until=%d", time.Now().Unix()))
	out, _, _ = runCommandWithOutput(eventsCmd)

	events := strings.Split(strings.TrimSpace(out), "\n")
	event := strings.TrimSpace(events[len(events)-1])

	if !strings.HasSuffix(event, ": import") {
		t.Fatalf("Missing pull event - got:%q", event)
	}

	logDone("events - image import is logged")
}

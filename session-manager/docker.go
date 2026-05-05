package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// dockerRun starts a DF container for the given user and returns the allocated host port and container ID.
func dockerRun(cfg *Config, uid, image string, containerPort int) (hostPort int, id string, err error) {
	saveDir := filepath.Join(cfg.SavesRoot, uid, "save")

	args := []string{
		"run", "-d",
		"--name", fmt.Sprintf("df-%s", uid),
		"--network", cfg.Network,
		"--cpus", "1.0",
		"--memory", "1g",
		"--pids-limit", "256",
		"--read-only",
		"--tmpfs", "/tmp:size=64m",
		"--mount", fmt.Sprintf("type=bind,source=%s,target=/save", saveDir),
		"-P", // publish exposed port to a random host port
		image,
	}

	out, err := runDocker(args...)
	if err != nil {
		return 0, "", fmt.Errorf("docker run: %w", err)
	}
	id = strings.TrimSpace(out)

	// Resolve the host port docker assigned.
	portOut, err := runDocker("port", id, strconv.Itoa(containerPort))
	if err != nil {
		_ = runDockerNoOut("rm", "-f", id)
		return 0, "", fmt.Errorf("docker port: %w", err)
	}
	// Output: 0.0.0.0:XXXXX
	parts := strings.Split(strings.TrimSpace(portOut), ":")
	p, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		_ = runDockerNoOut("rm", "-f", id)
		return 0, "", fmt.Errorf("parse port %q: %w", portOut, err)
	}
	return p, id, nil
}

// dockerStop sends SIGTERM to the container (triggers the quit-save script) then removes it.
func dockerStop(id string) error {
	// --time=15 gives the container 15 s after SIGTERM before SIGKILL.
	if err := runDockerNoOut("stop", "--time=15", id); err != nil {
		return err
	}
	return runDockerNoOut("rm", "-f", id)
}

func runDocker(args ...string) (string, error) {
	cmd := exec.Command("docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, stderr.String())
	}
	return stdout.String(), nil
}

func runDockerNoOut(args ...string) error {
	_, err := runDocker(args...)
	return err
}

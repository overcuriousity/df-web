package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// containerRuntime returns "podman" if CONTAINER_RUNTIME is set to it, or if
// "docker" is not found in PATH but "podman" is. Defaults to "docker".
func containerRuntime() string {
	if rt := os.Getenv("CONTAINER_RUNTIME"); rt != "" {
		return rt
	}
	if _, err := exec.LookPath("docker"); err != nil {
		if _, err := exec.LookPath("podman"); err == nil {
			return "podman"
		}
	}
	return "docker"
}

// ensureSaveDir creates the per-user save directory on the host with ownership
// 1000:1000 (DF's uid inside the container) and mode 0700. It is idempotent and
// corrects directories that Docker may have auto-created as root:root when the
// bind-mount source path was missing.
func ensureSaveDir(saveDir string) error {
	if err := os.MkdirAll(saveDir, 0o700); err != nil {
		return err
	}
	if err := os.Chown(saveDir, 1000, 1000); err != nil {
		return err
	}
	return os.Chmod(saveDir, 0o700)
}

// dockerRun starts a DF container for the given user and returns the container ID.
// The container is reachable at df-<uid>:<containerPort> on cfg.Network.
func dockerRun(cfg *Config, uid, image string) (id string, err error) {
	saveDir := filepath.Join(cfg.SavesRoot, uid, "save")
	if err := ensureSaveDir(saveDir); err != nil {
		return "", fmt.Errorf("ensure save dir %s: %w", saveDir, err)
	}
	name := fmt.Sprintf("df-%s", uid)

	// Remove any stopped container holding this name (e.g. one the s6 finish
	// script just took down after a DF crash). Docker reserves the name even
	// for exited containers, so without this we'd hit a name-conflict on the
	// very next /play. Errors are ignored: "no such container" is the common
	// case and not fatal.
	_ = runDockerNoOut("rm", "-f", name)

	args := []string{
		"run", "-d",
		"--name", name,
		"--network", cfg.Network,
		"--cpus", "1.0",
		"--memory", "4g",
		"--pids-limit", "256",
		// --read-only is intentionally omitted: DF writes errorlog.txt and
		// gamelog.txt to its working directory (/opt/df) on every run.
		// Security boundary is the container itself, network isolation, and
		// the per-user bind-mount for saves.
		"--tmpfs", "/tmp:size=64m",
		"--mount", fmt.Sprintf("type=bind,source=%s,target=/opt/df/data/save", saveDir),
		image,
	}

	out, err := runDocker(args...)
	if err != nil {
		return "", fmt.Errorf("docker run: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// dockerStop sends SIGTERM to the container (triggers the quit-save script) then removes it.
func dockerStop(id string) error {
	// --time=45 gives s6 → df/run trap → quit-sdl.sh → DF's save flow time
	// to finish writing the world to disk before SIGKILL.
	if err := runDockerNoOut("stop", "--time=45", id); err != nil {
		return err
	}
	return runDockerNoOut("rm", "-f", id)
}

func runDocker(args ...string) (string, error) {
	rt := containerRuntime()
	cmd := exec.Command(rt, args...)
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

// dockerListRunning returns the ID and name of every running container whose
// name starts with "df-".
func dockerListRunning() ([]struct{ id, name string }, error) {
	out, err := runDocker("ps", "--filter", "name=df-", "--format", "{{.ID}}\t{{.Names}}")
	if err != nil {
		return nil, err
	}
	var result []struct{ id, name string }
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) == 2 {
			result = append(result, struct{ id, name string }{parts[0], parts[1]})
		}
	}
	return result, nil
}

// dockerIsRunning reports whether the container with the given ID is currently
// running. Returns false (no error) for unknown or stopped containers.
func dockerIsRunning(id string) bool {
	out, err := runDocker("inspect", "--format", "{{.State.Running}}", id)
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) == "true"
}

// dockerRemoveExited removes all exited df-* containers.
func dockerRemoveExited() {
	out, err := runDocker("ps", "-a", "--filter", "name=df-", "--filter", "status=exited", "--format", "{{.ID}}")
	if err != nil {
		return
	}
	for _, id := range strings.Split(strings.TrimSpace(out), "\n") {
		if id != "" {
			_ = runDockerNoOut("rm", "-f", id)
		}
	}
}

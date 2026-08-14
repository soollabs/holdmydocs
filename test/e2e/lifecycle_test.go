package e2e

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestCompiledServerLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal lifecycle is Unix-specific")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "hmd")
	build := exec.Command("go", "build", "-o", binary, "./cmd/hmd")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, output)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	command := exec.Command(binary)
	command.Env = append(os.Environ(),
		"HMD_BIND="+address,
		"HMD_APP_DIR="+filepath.Join(data, "app"),
		"HMD_REPO_DIR="+filepath.Join(data, "repo"),
		"HMD_ADMIN_USER=admin",
		"HMD_ADMIN_PASSWORD=password12345",
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	defer func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
	}()

	ready := "http://" + address + "/_/ready"
	deadline := time.Now().Add(15 * time.Second)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, requestErr := http.Get(ready)
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not become ready at %s", ready)
		}
		<-ticker.C
	}

	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server exit: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not stop gracefully")
	}
}

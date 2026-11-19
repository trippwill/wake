// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package appliance_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

var quadlet = flag.Bool(
	"quadlet",
	false,
	"install from a loopback HTTP URL and run the rootless user service",
)

func TestAppliance(t *testing.T) {
	work := t.TempDir()

	image := os.Getenv("WAKE_IMAGE")
	if image == "" {
		image = "localhost/wake:dev"
	}

	app := appliance{
		name:  "wake-appliance-test-" + strings.ToLower(rand.Text()),
		image: image,
		data:  filepath.Join(work, "chosen host path"),
		port:  8787,
	}
	must(t, os.Mkdir(app.data, 0o700))
	t.Cleanup(func() { app.cleanup(t) })

	t.Log("Starting with an empty store")

	if *quadlet {
		app.installQuadlet(t)
	}

	app.start(t)

	if *quadlet {
		wanted := command(t, "systemctl", "--user", "show", app.name, "--property=WantedBy", "--value")
		require(t, slices.Contains(strings.Fields(string(wanted)), "default.target"), "missing boot target")
	}

	login, body := app.request(t, "/login", nil, nil)
	require(t, login.StatusCode == http.StatusOK && strings.Contains(body, "Set your Wake password"),
		"missing setup page")

	config := filepath.Join(app.data, "config", "config.toml")
	key := filepath.Join(app.data, "config", "credentials.key")
	auth := filepath.Join(app.data, "config", "auth.json")
	originalConfig, originalKey := readFile(t, config), readFile(t, key)
	require(t, len(originalKey) == 32, "missing generated credential key")
	require(t, fileInfo(t, key).Mode().Perm() == 0o400, "incorrect key permissions")

	for _, name := range []string{"config", "financial", "state", "backups"} {
		info := fileInfo(t, filepath.Join(app.data, name))
		require(t, info.Mode().Perm() == 0o700, "incorrect permissions for "+name)
		require(t, int(info.Sys().(*syscall.Stat_t).Uid) == os.Getuid(), "incorrect host ownership for "+name)
	}

	t.Log("Setting and rotating the live browser password and creating a client")

	const (
		firstPassword = "synthetic first appliance password"
		password      = "synthetic replacement appliance password"
	)

	app.password(t, firstPassword)
	oldCookie := app.login(t, firstPassword)
	app.password(t, password)

	response, _ := app.request(t, "/", nil, oldCookie)
	require(t, response.StatusCode == http.StatusSeeOther && response.Header.Get("Location") == "/login",
		"reset retained old browser access")

	const clientsPath = "/system/clients"

	cookie := app.login(t, password)
	response, page := app.request(t, clientsPath, nil, cookie)
	require(t, response.StatusCode == http.StatusOK, "could not open the API clients page")

	csrf := capture(t, `name="csrf" value="([^"]+)"`, page, "client form CSRF value")
	form := url.Values{"csrf": {csrf}, "name": {"Synthetic appliance client"}}
	response, page = app.request(t, clientsPath, form, cookie)
	require(t, response.StatusCode == http.StatusOK, "could not create the synthetic API client")

	token := capture(t, `id="client-token"[^>]*>([^<]+)</textarea>`, page, "issued client token")
	must(t, os.WriteFile(filepath.Join(app.data, "config", "test.token"), []byte(token), 0o600))

	checkClient := func() {
		command(t, "podman", "exec", app.name, "wake", "api", "check", "--token-file", "/data/config/test.token")
	}

	checkClient()

	originalAuth := readFile(t, auth)
	app.stop(t)

	t.Log("Checking configuration, access and retained evidence across replacement containers")

	// Seed immutable synthetic evidence only while the daemon is stopped.
	warehouse := filepath.Join(app.data, "financial", "wake.db")
	seedEvidence(t, warehouse)
	baseline := readEvidence(t, warehouse)

	for range 2 {
		app.start(t)
		app.login(t, password)
		checkClient()
		require(t, bytes.Equal(readFile(t, config), originalConfig), "restart changed configuration")
		require(t, bytes.Equal(readFile(t, key), originalKey), "restart changed credential key")
		require(t, bytes.Equal(readFile(t, auth), originalAuth), "restart changed access state")
		require(t, readEvidence(t, warehouse) == baseline, "restart changed retained evidence")
		app.stop(t)
	}

	t.Log("Taking an online warehouse snapshot and restoring it to a separate store")

	changed := bytes.ReplaceAll(originalConfig, []byte("port = 8787"),
		[]byte("port = 9787\nallowed_origins = ['https://wake.example', 'http://localhost:18787']"))
	require(t, !bytes.Equal(changed, originalConfig), "the initial configuration omitted the admin port")
	must(t, os.WriteFile(config, changed, 0o600))

	app.port = 9787
	app.adminHost = "wake.example"
	app.start(t)
	response, _ = app.request(t, "/login", nil, nil)
	require(t, response.StatusCode == http.StatusOK, "login page rejected the configured authority")
	command(
		t,
		"podman",
		"exec",
		app.name,
		"wake",
		"storage",
		"snapshot",
		"/data/backups/history.tar",
	)
	app.stop(t)

	restored := filepath.Join(work, "restored")
	must(t, os.MkdirAll(filepath.Join(restored, "backups"), 0o700))
	snapshot := readFile(t, filepath.Join(app.data, "backups", "history.tar"))
	must(t, os.WriteFile(filepath.Join(restored, "backups", "history.tar"), snapshot, 0o600))

	command(t, containerRun(restored, image, "config", "init",
		"--set", "storage.financial_path=/data/financial/wake.db",
		"--set", "storage.credentials_path=/data/state/connections.db",
		"--set", "paths.runtime_dir=/run/wake")...)
	command(t, containerRun(restored, image, "storage", "restore", "financial", "/data/backups/history.tar")...)
	require(t, readEvidence(t, filepath.Join(restored, "financial", "wake.db")) == baseline,
		"snapshot lost lineage or evidence")

	_, err := os.Stat(filepath.Join(restored, "config", "credentials.key"))
	require(t, errors.Is(err, os.ErrNotExist), "maintenance unexpectedly provisioned a key")

	t.Log("Checking startup and maintenance failures")
	checkInvalidInstallation(t, work, image, originalConfig)
}

type appliance struct {
	name, image, data string
	adminHost         string
	unit              string
	port, stops       int
}

func (app *appliance) start(t *testing.T) {
	t.Helper()

	if app.unit != "" {
		command(t, "systemctl", "--user", "start", app.name)
	} else {
		command(t, "podman", "run", "--detach", "--name", app.name, "--network=none", "--read-only",
			"--cap-drop=all", "--security-opt=no-new-privileges",
			"--tmpfs=/run/wake:rw,nosuid,nodev,noexec,mode=0700", "--volume", app.data+":/data:Z", app.image)
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := run("podman", "exec", app.name, "healthcheck"); err == nil {
			return
		}

		time.Sleep(500 * time.Millisecond)
	}

	t.Fatal("appliance did not become healthy")
}

func (app *appliance) stop(t *testing.T) {
	t.Helper()

	const stopped = `"event":"daemon_stopped"`

	if app.unit != "" {
		command(t, "systemctl", "--user", "stop", app.name)
		app.stops++

		logs := command(t, "journalctl", "--user", "--unit", app.name, "--output=cat", "--no-pager")
		require(t, bytes.Count(logs, []byte(stopped)) == app.stops, "missing joined service shutdown")

		return
	}

	command(t, "podman", "stop", "--time=60", app.name)
	logs := command(t, "podman", "logs", app.name)
	require(t, bytes.Contains(logs, []byte(stopped)), "missing joined container shutdown")
	command(t, "podman", "rm", app.name)
}

func (app *appliance) installQuadlet(t *testing.T) {
	t.Helper()

	for _, port := range []int{8787, 8788} {
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		must(t, err)
		must(t, listener.Close())
	}

	source := strings.Replace(
		string(readFile(t, "wake.container")),
		"ContainerName=wake",
		"ContainerName="+app.name,
		1,
	)

	configRoot, err := os.UserConfigDir()
	must(t, err)

	unit := filepath.Join(configRoot, "containers", "systemd", app.name+".container")
	dropIns := unit + ".d"
	must(t, os.MkdirAll(dropIns, 0o700))

	app.unit = unit

	storage := []byte("[Container]\nVolume=\nVolume=" + strings.ReplaceAll(app.data, "%", "%%") + ":/data:Z\n")
	image := []byte("[Container]\nImage=" + app.image + "\nPull=never\n")
	storagePath, imagePath := filepath.Join(dropIns, "storage.conf"), filepath.Join(dropIns, "image.conf")
	must(t, os.WriteFile(storagePath, storage, 0o600))
	must(t, os.WriteFile(imagePath, image, 0o600))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, source)
	}))
	defer server.Close()

	unitURL := server.URL + "/" + app.name + ".container"
	command(t, "podman", "quadlet", "install", unitURL)
	command(t, "podman", "quadlet", "install", "--replace", unitURL)
	require(
		t,
		bytes.Equal(readFile(t, storagePath), storage),
		"Quadlet replacement changed the storage override",
	)
	require(t, bytes.Equal(readFile(t, imagePath), image), "Quadlet replacement changed the image override")
}

func (app *appliance) cleanup(t *testing.T) {
	t.Helper()

	if t.Failed() {
		logs, _ := run("podman", "logs", app.name)
		t.Logf("Appliance logs:\n%s", logs)

		if app.unit != "" {
			logs, _ = run("journalctl", "--user", "--unit", app.name, "--no-pager", "-n", "60")
			t.Logf("Service logs:\n%s", logs)
		}
	}

	if app.unit != "" {
		_, _ = run("systemctl", "--user", "stop", app.name)

		if err := os.Remove(app.unit); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Error(err)
		}

		if err := os.RemoveAll(app.unit + ".d"); err != nil {
			t.Error(err)
		}

		_, _ = run("systemctl", "--user", "daemon-reload")
		_, _ = run("systemctl", "--user", "reset-failed", app.name)
	}

	_, _ = run("podman", "rm", "--force", app.name)
}

func (app *appliance) request(
	t *testing.T,
	path string,
	form url.Values,
	cookie *http.Cookie,
) (*http.Response, string) {
	t.Helper()

	origin := fmt.Sprintf("http://127.0.0.1:%d", app.port)
	args := []string{
		"exec", app.name, "curl", "--noproxy", "*", "--silent", "--show-error",
		"--include", "--raw", "--max-time", "5",
	}
	if app.adminHost != "" {
		args = append(args, "--header", "Host: "+app.adminHost)
	}

	if form != nil {
		args = append(args, "--header", "Origin: "+origin, "--data", form.Encode())
	}

	if cookie != nil {
		args = append(args, "--cookie", cookie.Name+"="+cookie.Value)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	//nolint:gosec // The target is the synthetic Wake loopback server inside the test container.
	cmd := exec.CommandContext(ctx, "podman", append(args, origin+path)...)

	var diagnostics bytes.Buffer

	cmd.Stderr = &diagnostics

	raw, err := cmd.Output()
	if err != nil {
		t.Fatalf("HTTP request to %s failed: %v\n%s", path, err, &diagnostics)
	}

	response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), nil)
	must(t, err)

	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(response.Body)
	must(t, err)

	return response, string(body)
}

func (app *appliance) login(t *testing.T, password string) *http.Cookie {
	t.Helper()

	// Wake intentionally paces password attempts.
	time.Sleep(1100 * time.Millisecond)

	response, _ := app.request(t, "/login", url.Values{"password": {password}}, nil)
	require(t, response.StatusCode == http.StatusSeeOther, "browser sign-in failed")

	cookies := response.Cookies()
	require(t, len(cookies) == 1, "browser sign-in did not issue a session cookie")

	return cookies[0]
}

func (app *appliance) password(t *testing.T, password string) {
	t.Helper()

	master, slave := openTerminal(t)
	defer func() { _ = master.Close() }()
	defer func() { _ = slave.Close() }()

	deadline := time.Now().Add(30 * time.Second)
	must(t, master.SetDeadline(deadline))

	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()

	//nolint:gosec // The container name is generated for this test.
	cmd := exec.CommandContext(ctx, "podman", "exec", "-it", app.name, "wake", "access", "set", "password")

	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	must(t, cmd.Start())
	defer func() {
		if cmd.ProcessState == nil {
			cancel()

			_ = cmd.Wait()
		}
	}()

	must(t, slave.Close())

	var (
		output bytes.Buffer
		buf    [4096]byte
	)

	for _, prompt := range []string{"Password: ", "Confirm password: "} {
		for !bytes.Contains(output.Bytes(), []byte(prompt)) {
			n, err := master.Read(buf[:])
			output.Write(buf[:n])

			if err != nil {
				t.Fatalf("reading password prompt %q: %v", prompt, err)
			}
		}

		_, err := fmt.Fprintln(master, password)
		must(t, err)
	}

	_, err := io.Copy(&output, master)
	if !errors.Is(err, unix.EIO) {
		must(t, err)
	}

	require(t, !bytes.Contains(output.Bytes(), []byte(password)), "terminal echoed the password")
	must(t, cmd.Wait())
}

func openTerminal(t *testing.T) (*os.File, *os.File) {
	t.Helper()

	// A nonblocking master lets Go set a deadline while waiting for interactive prompts.
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	must(t, err)

	master := os.NewFile(uintptr(fd), "/dev/ptmx")
	t.Cleanup(func() { _ = master.Close() })

	must(t, unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0))
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	must(t, err)

	//nolint:gosec // The kernel supplies the terminal number.
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	must(t, err)

	return master, slave
}

func seedEvidence(t *testing.T, path string) {
	t.Helper()

	db := openDB(t, path, "mode=rw&_pragma=foreign_keys(ON)")
	defer func() { _ = db.Close() }()

	tx, err := db.BeginTx(t.Context(), nil)
	must(t, err)

	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(t.Context(), "INSERT INTO connections VALUES(?,?,?)",
		"appliance-source", "synthetic", "Appliance test")
	must(t, err)

	payload := []byte("Synthetic appliance persistence and snapshot evidence")
	_, err = tx.ExecContext(t.Context(),
		"INSERT INTO artifacts(id,connection_id,stream,operation,format,media_type,retrieved,hash,content) "+
			"VALUES(?,?,?,?,?,?,?,?,?)",
		"appliance-report", "appliance-source", "history", "discover", "synthetic/1", "text/plain",
		"2026-01-01T00:00:00Z", fmt.Sprintf("%x", sha256.Sum256(payload)), payload)
	must(t, err)
	must(t, tx.Commit())
}

type evidence struct{ lineage, payload string }

func readEvidence(t *testing.T, path string) evidence {
	t.Helper()

	db := openDB(t, path, "mode=ro")
	defer func() { _ = db.Close() }()

	var stored evidence

	must(t, db.QueryRowContext(t.Context(), "SELECT id FROM financial_lineage").Scan(&stored.lineage))
	must(t, db.QueryRowContext(t.Context(),
		"SELECT content FROM artifacts WHERE id='appliance-report'").Scan(&stored.payload))

	return stored
}

func openDB(t *testing.T, path, query string) *sql.DB {
	t.Helper()

	uri := (&url.URL{Scheme: "file", Path: path, RawQuery: query}).String()
	db, err := sql.Open("sqlite", uri)
	must(t, err)

	return db
}

func checkInvalidInstallation(t *testing.T, work, image string, configuration []byte) {
	t.Helper()

	data := filepath.Join(work, "invalid installation")
	configDir := filepath.Join(data, "config")
	config := filepath.Join(configDir, "config.toml")
	must(t, os.MkdirAll(configDir, 0o700))
	must(t, os.WriteFile(config, configuration, 0o600))

	fails := func(message string, args ...string) {
		t.Helper()

		output, err := run(containerRun(data, image, args...)...)
		if err == nil || !bytes.Contains(output, []byte(message)) {
			t.Fatalf("expected appliance failure mentioning %q: %v\n%s", message, err, output)
		}
	}

	//nolint:gosec // The first run verifies that Wake rejects this directory mode.
	must(t, os.Chmod(configDir, 0o755))
	fails("must not be accessible by group or other users")
	//nolint:gosec // The directory needs its owner execute bit for traversal.
	must(t, os.Chmod(configDir, 0o700))

	func() {
		command(t, "podman", "unshare", "chown", "1", configDir)
		defer command(t, "podman", "unshare", "chown", "0", configDir)

		fails("/data/config")
	}()

	must(t, os.WriteFile(config, []byte("[admin]\nport = -1\n"), 0o600))
	fails("admin.port")
	must(t, os.WriteFile(config, configuration, 0o600))

	must(t, os.WriteFile(filepath.Join(data, "financial", "wake.db"), []byte("Not a SQLite database"), 0o600))
	fails("unsupported store format")
	fails("/data/config/credentials.key", "config", "check", "--check-key")
}

func containerRun(data, image string, args ...string) []string {
	return append([]string{
		"podman", "run", "--rm", "--network=none", "--read-only", "--cap-drop=all",
		"--security-opt=no-new-privileges", "--volume", data + ":/data:Z", image,
	}, args...)
}

func run(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	//nolint:gosec // Callers supply fixed command names and direct argument lists.
	return exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
}

func command(t *testing.T, args ...string) []byte {
	t.Helper()

	output, err := run(args...)
	if err != nil {
		t.Fatalf("%q: %v\n%s", args, err, output)
	}

	return output
}

func capture(t *testing.T, pattern, body, field string) string {
	t.Helper()

	match := regexp.MustCompile(pattern).FindStringSubmatch(body)
	require(t, len(match) == 2, "missing "+field)

	return match[1]
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()

	//nolint:gosec // Callers read fixtures or paths in the disposable store.
	data, err := os.ReadFile(path)
	must(t, err)

	return data
}

func fileInfo(t *testing.T, path string) os.FileInfo {
	t.Helper()

	info, err := os.Stat(path)
	must(t, err)

	return info
}

func require(t *testing.T, condition bool, message string) {
	t.Helper()

	if !condition {
		t.Fatal(message)
	}
}

func must(t *testing.T, err error) {
	t.Helper()

	if err != nil {
		t.Fatal(err)
	}
}

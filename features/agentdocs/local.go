package agentdocs

// 一键启动: Crewbase downloads a pinned Open Agent Docs release, checks it
// against the hash below, and runs it on 127.0.0.1 with its data in
// <root>/apps/open-agent-docs. The person picks the email and password
// they sign in to its web app with; Crewbase signs in once with them to
// create its admin agent, then connects as usual. The password is not kept.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Release is the Open Agent Docs version 一键启动 installs.
const Release = "v0.2.0"

// releaseHashes are the SHA-256 of Release's archives (its SHA256SUMS,
// checked when the version was pinned): a download that differs is refused.
var releaseHashes = map[string]string{
	"darwin/arm64": "471444c477151288ddd3436c4c4a6852212f7f799b8d629fd2a79562661f7990",
	"darwin/amd64": "ec2934dbdd19cead5a9288db8f87d8a41b57f36db6ccda36ee2ef66d60f035ed",
	"linux/arm64":  "daf8dca6eeb0e8e55ffe82c66a84cf64461f1f9974f69bcdc0c795b3de58da61",
	"linux/amd64":  "a9dc8ce5ac1468cf522f67dd1fd7614df6d26e167dbbaca4751747db6f5d6872",
}

var releaseTriples = map[string]string{
	"darwin/arm64": "aarch64-apple-darwin",
	"darwin/amd64": "x86_64-apple-darwin",
	"linux/arm64":  "aarch64-unknown-linux-gnu",
	"linux/amd64":  "x86_64-unknown-linux-gnu",
}

// releaseBase is where the archives are downloaded from (tests point it
// at a local server).
var releaseBase = "https://github.com/kadaliao/open-agent-docs/releases/download"

const maxArchive = 64 << 20

// Managed is the server Crewbase runs (agentdocs.json).
type Managed struct {
	Version string `json:"version"`
	Port    int    `json:"port"`
	Email   string `json:"email"` // the person's sign-in
	PID     int    `json:"pid,omitempty"`
}

// LocalStatus is the API view of the managed server.
type LocalStatus struct {
	Supported bool   `json:"supported"`
	Release   string `json:"release"`
	Running   bool   `json:"running"`
	URL       string `json:"url,omitempty"`
	Email     string `json:"email,omitempty"`
	Version   string `json:"version,omitempty"`
	// Step while starting: download | start | account | connect.
	Step  string `json:"step,omitempty"`
	Error string `json:"error,omitempty"`
}

type local struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	stop    chan struct{}
	step    string
	err     string
	running bool
}

func platform() string { return runtime.GOOS + "/" + runtime.GOARCH }

// LocalSupported reports whether 一键启动 has a build for this computer.
func LocalSupported() bool { _, ok := releaseHashes[platform()]; return ok }

func (s *Service) appDir() string { return filepath.Join(s.dir, "apps", "open-agent-docs") }

func (s *Service) binPath(version string) string {
	return filepath.Join(s.appDir(), version, "open-agent-docs")
}

// Local reports the managed server.
func (s *Service) Local() LocalStatus {
	s.mu.Lock()
	m := s.st.Managed
	s.mu.Unlock()
	s.lc.mu.Lock()
	defer s.lc.mu.Unlock()
	st := LocalStatus{Supported: LocalSupported(), Release: Release, Running: s.lc.running, Step: s.lc.step, Error: s.lc.err}
	if m != nil {
		st.URL, st.Email, st.Version = localURL(m.Port), m.Email, m.Version
	}
	return st
}

func localURL(port int) string { return "http://127.0.0.1:" + strconv.Itoa(port) }

func (s *Service) setStep(step, err string) {
	s.lc.mu.Lock()
	s.lc.step, s.lc.err = step, err
	s.lc.mu.Unlock()
}

// StartLocalRequest is what 一键启动 asks for.
type StartLocalRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// StartLocal installs the pinned release (when missing), runs it, creates
// the person's account and Crewbase's admin agent, and connects.
func (s *Service) StartLocal(ctx context.Context, r StartLocalRequest) (Status, error) {
	if !LocalSupported() {
		return Status{}, fmt.Errorf("Open Agent Docs has no build for %s yet; run it with Docker and connect it by its address", platform())
	}
	email := strings.TrimSpace(r.Email)
	if !strings.Contains(email, "@") {
		return Status{}, errors.New("give the email you will sign in to the docs with")
	}
	if len([]rune(r.Password)) < 8 {
		return Status{}, errors.New("the password needs at least 8 characters")
	}
	s.mu.Lock()
	if s.st.Managed != nil {
		s.mu.Unlock()
		return Status{}, errors.New("Crewbase already runs an Open Agent Docs server; disconnect it first")
	}
	s.mu.Unlock()
	fail := func(step string, err error) (Status, error) {
		s.setStep("", err.Error())
		return Status{}, err
	}

	s.setStep("download", "")
	bin, err := s.install(ctx)
	if err != nil {
		return fail("download", err)
	}
	port, err := freePort(8848)
	if err != nil {
		return fail("start", err)
	}
	m := &Managed{Version: Release, Port: port, Email: email}
	s.setStep("start", "")
	// the first start creates the account (ignored once any user exists)
	if err := s.run(bin, m, map[string]string{"OAD_ADMIN_EMAIL": email, "OAD_ADMIN_PASSWORD": r.Password}); err != nil {
		return fail("start", err)
	}
	base := localURL(port)
	if err := waitUp(ctx, base, 30*time.Second); err != nil {
		s.stopLocal()
		return fail("start", err)
	}
	s.setStep("account", "")
	token, err := createAdminAgent(ctx, base, email, r.Password, s.hasLocalData())
	if err != nil {
		s.stopLocal()
		return fail("account", err)
	}
	s.mu.Lock()
	s.st.Managed = m
	err = s.saveLocked()
	s.mu.Unlock()
	if err != nil {
		return fail("account", err)
	}
	// later starts need no password: restart supervised, without the admin env
	s.stopLocal()
	if err := s.run(bin, m, nil); err != nil {
		return fail("start", err)
	}
	if err := waitUp(ctx, base, 30*time.Second); err != nil {
		return fail("start", err)
	}
	s.setStep("connect", "")
	if _, err := s.Connect(ctx, ConnectRequest{URL: base, Token: token}); err != nil {
		return fail("connect", err)
	}
	s.setStep("", "")
	return s.Status(), nil
}

// install downloads, checks and unpacks the pinned release (once).
func (s *Service) install(ctx context.Context) (string, error) {
	bin := s.binPath(Release)
	if fi, err := os.Stat(bin); err == nil && fi.Mode().IsRegular() {
		return bin, nil
	}
	plat := platform()
	name := fmt.Sprintf("open-agent-docs-%s-%s", Release, releaseTriples[plat])
	req, err := http.NewRequestWithContext(ctx, "GET", releaseBase+"/"+Release+"/"+name+".tar.gz", nil)
	if err != nil {
		return "", err
	}
	c := &http.Client{Timeout: 5 * time.Minute}
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not download Open Agent Docs: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("could not download Open Agent Docs: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxArchive+1))
	if err != nil {
		return "", fmt.Errorf("could not download Open Agent Docs: %w", err)
	}
	if len(data) > maxArchive {
		return "", errors.New("the Open Agent Docs download is larger than expected")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != releaseHashes[plat] {
		return "", errors.New("the Open Agent Docs download does not match its published checksum; nothing was installed")
	}
	// only the binary is taken from the archive
	zr, err := gzip.NewReader(strings.NewReader(string(data)))
	if err != nil {
		return "", err
	}
	tr := tar.NewReader(zr)
	want := name + "/open-agent-docs"
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return "", err
	}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return "", errors.New("the Open Agent Docs archive has no server binary")
		}
		if err != nil {
			return "", err
		}
		if path.Clean(h.Name) != want || h.Typeflag != tar.TypeReg {
			continue
		}
		tmp := bin + ".tmp"
		f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			return "", err
		}
		_, err = io.Copy(f, io.LimitReader(tr, maxArchive))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(tmp)
			return "", err
		}
		return bin, os.Rename(tmp, bin)
	}
}

// run starts the server and restarts it when it exits (until stopLocal).
func (s *Service) run(bin string, m *Managed, extra map[string]string) error {
	s.killStale(m)
	data := filepath.Join(s.appDir(), "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(s.appDir(), "server.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	start := func() (*exec.Cmd, error) {
		cmd := exec.Command(bin, "serve", "--host", "127.0.0.1", "--port", strconv.Itoa(m.Port), "--data-dir", data, "--public-url", localURL(m.Port))
		cmd.Env = append(os.Environ(), "RUST_LOG=warn")
		for k, v := range extra {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		cmd.Stdout, cmd.Stderr = logf, logf
		cmd.SysProcAttr = sysProcAttr()
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		s.mu.Lock()
		m.PID = cmd.Process.Pid
		if s.st.Managed == m {
			_ = s.saveLocked()
		}
		s.mu.Unlock()
		return cmd, nil
	}
	cmd, err := start()
	if err != nil {
		logf.Close()
		return fmt.Errorf("could not start Open Agent Docs: %w", err)
	}
	stop := make(chan struct{})
	s.lc.mu.Lock()
	s.lc.cmd, s.lc.stop, s.lc.running = cmd, stop, true
	s.lc.mu.Unlock()
	go func() {
		defer logf.Close()
		backoff := time.Second
		for {
			cmd.Wait()
			select {
			case <-stop:
				return
			default:
			}
			s.lc.mu.Lock()
			s.lc.running = false
			s.lc.mu.Unlock()
			select {
			case <-stop:
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, time.Minute)
			next, err := start()
			if err != nil {
				s.setStep("", "Open Agent Docs stopped and could not start again: "+err.Error())
				continue
			}
			s.lc.mu.Lock()
			s.lc.cmd, s.lc.running = next, true
			s.lc.mu.Unlock()
			cmd = next
		}
	}()
	return nil
}

// killStale stops a server a previous Crewbase run left behind.
func (s *Service) killStale(m *Managed) {
	if m.PID <= 0 {
		return
	}
	p, err := os.FindProcess(m.PID)
	if err != nil {
		return
	}
	// only our own binary: the pid may have been reused
	if out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(m.PID)).Output(); err == nil && strings.Contains(string(out), "open-agent-docs") {
		_ = p.Signal(syscall.SIGTERM)
		time.Sleep(500 * time.Millisecond)
	}
}

func (s *Service) stopLocal() {
	s.lc.mu.Lock()
	cmd, stop := s.lc.cmd, s.lc.stop
	s.lc.cmd, s.lc.stop, s.lc.running = nil, nil, false
	s.lc.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _, _ = cmd.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
		}
	}
}

// resumeLocal starts the managed server again when Crewbase starts.
func (s *Service) resumeLocal(ctx context.Context) {
	s.mu.Lock()
	m := s.st.Managed
	s.mu.Unlock()
	if m == nil {
		return
	}
	bin := s.binPath(m.Version)
	if _, err := os.Stat(bin); err != nil {
		s.setStep("", "the Open Agent Docs server is missing; disconnect and start it again")
		return
	}
	if err := s.run(bin, m, nil); err != nil {
		s.setStep("", err.Error())
		return
	}
	if err := waitUp(ctx, localURL(m.Port), 30*time.Second); err != nil {
		s.setStep("", err.Error())
	}
}

// removeLocal stops the managed server; its data stays unless wipe.
func (s *Service) removeLocal(wipe bool) {
	s.stopLocal()
	s.mu.Lock()
	s.st.Managed = nil
	_ = s.saveLocked()
	s.mu.Unlock()
	if wipe {
		_ = os.RemoveAll(filepath.Join(s.appDir(), "data"))
	}
}

func freePort(prefer int) (int, error) {
	for _, p := range []int{prefer, 0} {
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err != nil {
			continue
		}
		port := l.Addr().(*net.TCPAddr).Port
		l.Close()
		return port, nil
	}
	return 0, errors.New("no free port on 127.0.0.1")
}

func waitUp(ctx context.Context, base string, d time.Duration) error {
	deadline := time.Now().Add(d)
	c := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, "GET", base+"/api/v1/auth/config", nil)
		if resp, err := c.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("Open Agent Docs did not start in time (see apps/open-agent-docs/server.log in the data folder)")
}

// hasLocalData: a server ran here before (its account already exists).
func (s *Service) hasLocalData() bool {
	_, err := os.Stat(filepath.Join(s.appDir(), "data", "oad.db"))
	return err == nil
}

// createAdminAgent signs in as the person and creates Crewbase's admin agent.
func createAdminAgent(ctx context.Context, base, email, password string, existing bool) (string, error) {
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Timeout: 20 * time.Second, Jar: jar}
	do := func(method, p string, in, out any) error {
		var body io.Reader
		if in != nil {
			b, _ := json.Marshal(in)
			body = strings.NewReader(string(b))
		}
		req, err := http.NewRequestWithContext(ctx, method, base+"/api/v1"+p, body)
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", base) // the server checks it on changes
		resp, err := c.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode >= 300 {
			var e struct {
				Error struct{ Message string } `json:"error"`
			}
			_ = json.Unmarshal(b, &e)
			if e.Error.Message == "" {
				e.Error.Message = resp.Status
			}
			return fmt.Errorf("docs server: %s", e.Error.Message)
		}
		if out != nil {
			return json.Unmarshal(b, out)
		}
		return nil
	}
	if err := do("POST", "/auth/login", map[string]string{"email": email, "password": password}, nil); err != nil {
		if existing {
			return "", errors.New("the docs server kept its data from before: sign in with the email and password you used then")
		}
		return "", fmt.Errorf("could not sign in to the new docs server: %w", err)
	}
	var me meOut
	if err := do("GET", "/me", nil, &me); err != nil {
		return "", err
	}
	if len(me.Workspaces) == 0 {
		return "", errors.New("the new docs server has no workspace")
	}
	var created struct {
		Token string `json:"token"`
	}
	var err error
	for i := 1; i <= 9; i++ {
		name := "Crewbase"
		if i > 1 {
			name = fmt.Sprintf("Crewbase-%d", i) // an earlier connection's agent
		}
		err = do("POST", "/workspaces/"+me.Workspaces[0].ID+"/agents", map[string]string{"name": name, "scope": "admin", "about": "Crewbase: creates and revokes the identities of its agents"}, &created)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "exist") && !strings.Contains(err.Error(), "已") && !strings.Contains(err.Error(), "409") {
			break
		}
	}
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(created.Token, "oad_") {
		return "", errors.New("the docs server returned no token")
	}
	return created.Token, nil
}

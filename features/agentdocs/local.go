package agentdocs

// 一键启动: Crewbase downloads the latest Open Agent Docs release, checks
// it against the SHA256SUMS published with it, and runs it on 127.0.0.1
// with its data in <root>/apps/open-agent-docs. It follows upstream: a
// newer release is offered as an update (UpdateLocal). The person picks the email and password
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
	"net/url"
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

var releaseTriples = map[string]string{
	"darwin/arm64": "aarch64-apple-darwin",
	"darwin/amd64": "x86_64-apple-darwin",
	"linux/arm64":  "aarch64-unknown-linux-gnu",
	"linux/amd64":  "x86_64-unknown-linux-gnu",
}

// releaseAPI is the latest release (tests point it at a local server).
var releaseAPI = "https://api.github.com/repos/kadaliao/open-agent-docs/releases/latest"

// Release is an upstream release with a build for this computer.
type Release struct {
	Tag       string    `json:"tag"`
	Notes     string    `json:"notes,omitempty"`
	Published time.Time `json:"published"`
	archive   string    // download URL of this platform's archive
	asset     string    // its file name
	sums      string    // download URL of SHA256SUMS
}

// latest asks GitHub for the newest release (cached for an hour).
func (s *Service) latest(ctx context.Context, fresh bool) (Release, error) {
	s.lc.mu.Lock()
	if !fresh && s.lc.rel.Tag != "" && time.Since(s.lc.relAt) < time.Hour {
		r := s.lc.rel
		s.lc.mu.Unlock()
		return r, nil
	}
	s.lc.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, "GET", releaseAPI, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("could not check for Open Agent Docs releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Release{}, fmt.Errorf("could not check for Open Agent Docs releases: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Tag       string    `json:"tag_name"`
		Body      string    `json:"body"`
		Draft     bool      `json:"draft"`
		Pre       bool      `json:"prerelease"`
		Published time.Time `json:"published_at"`
		Assets    []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return Release{}, err
	}
	if out.Tag == "" || out.Draft {
		return Release{}, errors.New("Open Agent Docs has no published release")
	}
	r := Release{Tag: out.Tag, Notes: clip(strings.TrimSpace(out.Body), 4000), Published: out.Published}
	suffix := "-" + releaseTriples[platform()] + ".tar.gz"
	for _, a := range out.Assets {
		switch {
		case a.Name == "SHA256SUMS":
			r.sums = a.URL
		case strings.HasPrefix(a.Name, "open-agent-docs-") && strings.HasSuffix(a.Name, suffix):
			r.archive, r.asset = a.URL, a.Name
		}
	}
	if r.archive == "" {
		return Release{}, fmt.Errorf("Open Agent Docs %s has no build for %s", r.Tag, platform())
	}
	if r.sums == "" {
		return Release{}, fmt.Errorf("Open Agent Docs %s publishes no checksums; not installing it", r.Tag)
	}
	s.lc.mu.Lock()
	s.lc.rel, s.lc.relAt = r, time.Now()
	s.lc.mu.Unlock()
	return r, nil
}

func download(ctx context.Context, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("larger than expected")
	}
	return b, nil
}

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
	Supported bool `json:"supported"`
	Running   bool `json:"running"`
	// Update: a newer release than the one running (checked hourly at most).
	Update  *Release `json:"update,omitempty"`
	URL     string   `json:"url,omitempty"`
	Email   string   `json:"email,omitempty"`
	Version string   `json:"version,omitempty"`
	// Step while starting: download | start | account | connect.
	Step  string `json:"step,omitempty"`
	Error string `json:"error,omitempty"`
}

type local struct {
	mu      sync.Mutex
	rel     Release // the latest release seen
	relAt   time.Time
	cmd     *exec.Cmd
	stop    chan struct{}
	step    string
	err     string
	running bool
}

func platform() string { return runtime.GOOS + "/" + runtime.GOARCH }

// LocalSupported reports whether 一键启动 has a build for this computer.
func LocalSupported() bool { _, ok := releaseTriples[platform()]; return ok }

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
	st := LocalStatus{Supported: LocalSupported(), Running: s.lc.running, Step: s.lc.step, Error: s.lc.err}
	if m != nil {
		st.URL, st.Email, st.Version = localURL(m.Port), m.Email, m.Version
		if r := s.lc.rel; r.Tag != "" && r.Tag != m.Version && newer(r.Tag, m.Version) {
			st.Update = &r
		}
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
	rel, err := s.latest(ctx, true)
	if err != nil {
		return fail("download", err)
	}
	bin, err := s.install(ctx, rel)
	if err != nil {
		return fail("download", err)
	}
	port, err := freePort(8848)
	if err != nil {
		return fail("start", err)
	}
	m := &Managed{Version: rel.Tag, Port: port, Email: email}
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
	token, session, err := createAdminAgent(ctx, base, email, r.Password, s.hasLocalData())
	if err != nil {
		s.stopLocal()
		return fail("account", err)
	}
	// the person's own session: signs the docs in when shown inside Crewbase
	if cs := s.creds(); cs != nil && session != "" {
		_ = cs.Set(sessionRef, session)
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

// install downloads rel, checks it against its SHA256SUMS and unpacks the
// server binary (once per release).
func (s *Service) install(ctx context.Context, rel Release) (string, error) {
	bin := s.binPath(rel.Tag)
	if fi, err := os.Stat(bin); err == nil && fi.Mode().IsRegular() {
		return bin, nil
	}
	sums, err := download(ctx, rel.sums, 64<<10)
	if err != nil {
		return "", fmt.Errorf("could not download the Open Agent Docs checksums: %w", err)
	}
	want := ""
	for _, l := range strings.Split(string(sums), "\n") {
		f := strings.Fields(l)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == rel.asset {
			want = strings.ToLower(f[0])
		}
	}
	if len(want) != 64 {
		return "", fmt.Errorf("the Open Agent Docs checksums do not list %s; not installing it", rel.asset)
	}
	data, err := download(ctx, rel.archive, maxArchive)
	if err != nil {
		return "", fmt.Errorf("could not download Open Agent Docs: %w", err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != want {
		return "", errors.New("the Open Agent Docs download does not match its published checksum; nothing was installed")
	}
	name := strings.TrimSuffix(rel.asset, ".tar.gz")
	// only the binary is taken from the archive
	zr, err := gzip.NewReader(strings.NewReader(string(data)))
	if err != nil {
		return "", err
	}
	tr := tar.NewReader(zr)
	binName := name + "/open-agent-docs"
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
		if path.Clean(h.Name) != binName || h.Typeflag != tar.TypeReg {
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
		// the run loop owns Wait; here: signal, then watch the pid until it is gone
		pid := cmd.Process.Pid
		_ = cmd.Process.Signal(syscall.SIGTERM)
		deadline := time.Now().Add(5 * time.Second)
		for alive(pid) && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if alive(pid) {
			_ = cmd.Process.Kill() // it waits for open requests (long polls) otherwise
			for i := 0; i < 40 && alive(pid); i++ {
				time.Sleep(50 * time.Millisecond)
			}
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
	if _, err := s.latest(ctx, false); err != nil { // so an update shows
		s.c.Log().Warn("agentdocs: could not check for a newer Open Agent Docs", "err", err)
	}
}

// CheckLocal looks for a newer release of the managed server.
func (s *Service) CheckLocal(ctx context.Context) (LocalStatus, error) {
	s.mu.Lock()
	managed := s.st.Managed != nil
	s.mu.Unlock()
	if !managed || !LocalSupported() {
		return s.Local(), nil
	}
	_, err := s.latest(ctx, true)
	return s.Local(), err
}

// UpdateLocal moves the managed server to the latest release: installs it,
// restarts on it (the data stays) and removes the old binary.
func (s *Service) UpdateLocal(ctx context.Context) (Status, error) {
	s.mu.Lock()
	m := s.st.Managed
	s.mu.Unlock()
	if m == nil {
		return Status{}, errors.New("Crewbase does not run an Open Agent Docs server")
	}
	rel, err := s.latest(ctx, true)
	if err != nil {
		return Status{}, err
	}
	if rel.Tag == m.Version || !newer(rel.Tag, m.Version) {
		return s.Status(), nil
	}
	s.setStep("download", "")
	bin, err := s.install(ctx, rel)
	if err != nil {
		s.setStep("", err.Error())
		return Status{}, err
	}
	old := m.Version
	s.setStep("start", "")
	s.stopListeners() // their long polls would hold the old server open
	defer s.restartListeners()
	s.stopLocal()
	s.mu.Lock()
	m.Version = rel.Tag
	_ = s.saveLocked()
	s.mu.Unlock()
	if err := s.run(bin, m, nil); err == nil {
		err = waitUp(ctx, localURL(m.Port), 30*time.Second)
		if err == nil {
			_ = os.RemoveAll(filepath.Dir(s.binPath(old)))
			s.setStep("", "")
			return s.Status(), nil
		}
	}
	// the new release did not come up: back to the old one
	s.stopLocal()
	s.mu.Lock()
	m.Version = old
	_ = s.saveLocked()
	s.mu.Unlock()
	if err := s.run(s.binPath(old), m, nil); err != nil {
		s.setStep("", err.Error())
		return Status{}, err
	}
	err = fmt.Errorf("Open Agent Docs %s did not start; still on the earlier version", rel.Tag)
	s.setStep("", err.Error())
	return Status{}, err
}

// newer reports whether tag a is a later version than b (v1.2.3).
func newer(a, b string) bool {
	pa, pb := verParts(a), verParts(b)
	for i := range 3 {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func verParts(v string) [3]int {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	for i, p := range strings.SplitN(v, ".", 3) {
		out[i], _ = strconv.Atoi(p)
	}
	return out
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
func createAdminAgent(ctx context.Context, base, email, password string, existing bool) (token, session string, err error) {
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
			return "", "", errors.New("the docs server kept its data from before: sign in with the email and password you used then")
		}
		return "", "", fmt.Errorf("could not sign in to the new docs server: %w", err)
	}
	if u, err := url.Parse(base); err == nil {
		for _, ck := range jar.Cookies(u) {
			if ck.Name == SessionCookie {
				session = ck.Value
			}
		}
	}
	var me meOut
	if err := do("GET", "/me", nil, &me); err != nil {
		return "", "", err
	}
	if len(me.Workspaces) == 0 {
		return "", "", errors.New("the new docs server has no workspace")
	}
	var created struct {
		Token string `json:"token"`
	}
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
		return "", "", err
	}
	if !strings.HasPrefix(created.Token, "oad_") {
		return "", "", errors.New("the docs server returned no token")
	}
	return created.Token, session, nil
}

// SessionCookie is the docs server's sign-in cookie.
const SessionCookie = "oad_session"

const sessionRef = "vault:crewbase/agentdocs-session"

// Embed is what the web UI needs to show the docs inside Crewbase.
type Embed struct {
	URL string `json:"url"`
	// SignedIn: the browser asking already has, or now gets, a session.
	SignedIn bool `json:"signedIn"`
}

// EmbedFor tells whether the browser that sent r is signed in to the docs
// and, when it is not but Crewbase holds the person's session (one-click
// start), returns the cookie that signs it in. Cookies are not separated
// by port, so one set here for 127.0.0.1 is the docs server's too; it is
// HttpOnly, like the docs server's own, so pages on other local ports
// cannot read it.
func (s *Service) EmbedFor(ctx context.Context, r *http.Request) (Embed, *http.Cookie) {
	s.mu.Lock()
	base, managed := s.st.Settings.URL, s.st.Managed != nil
	s.mu.Unlock()
	e := Embed{URL: base}
	if base == "" {
		return e, nil
	}
	valid := func(v string) bool {
		if v == "" {
			return false
		}
		req, err := http.NewRequestWithContext(ctx, "GET", base+"/api/v1/me", nil)
		if err != nil {
			return false
		}
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: v})
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	}
	if ck, err := r.Cookie(SessionCookie); err == nil && valid(ck.Value) {
		e.SignedIn = true
		return e, nil
	}
	cs := s.creds()
	if !managed || cs == nil {
		return e, nil
	}
	v, err := cs.Resolve(ctx, sessionRef)
	if err != nil || v == "" {
		return e, nil
	}
	if !valid(v) {
		_ = cs.Delete(sessionRef) // expired: signing in inside the page renews it
		return e, nil
	}
	e.SignedIn = true
	return e, &http.Cookie{Name: SessionCookie, Value: v, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 30 * 24 * 3600}
}

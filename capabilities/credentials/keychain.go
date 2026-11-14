package credentials

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

func lookupEnv(name string) string { return os.Getenv(name) }

// keychain ids are "<service>/<account>"; both parts are restricted so they
// can be passed to CLIs without quoting.
var keychainPart = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,128}$`)

func splitKeychainID(id string) (service, account string, err error) {
	service, account, ok := strings.Cut(id, "/")
	if !ok || !keychainPart.MatchString(service) || !keychainPart.MatchString(account) {
		return "", "", fmt.Errorf("%w: keychain ref must be keychain:<service>/<id> using [A-Za-z0-9._@-]", ErrUnsupported)
	}
	return service, account, nil
}

// systemKeychain returns the platform keychain backend, or nil.
func systemKeychain() Backend {
	switch runtime.GOOS {
	case "darwin":
		if p, err := exec.LookPath("security"); err == nil {
			return &MacKeychain{bin: p}
		}
	case "linux", "freebsd", "openbsd", "netbsd":
		if p, err := exec.LookPath("secret-tool"); err == nil {
			return &SecretTool{bin: p}
		}
	}
	return nil
}

// exitCode returns the process exit code, or -1.
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// run executes bin with args, feeding stdin. Neither stdin nor the
// command's output ever appears in returned errors.
func run(ctx context.Context, bin string, stdin []byte, args ...string) (stdout []byte, stderr []byte, err error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"),
		"DBUS_SESSION_BUS_ADDRESS=" + os.Getenv("DBUS_SESSION_BUS_ADDRESS")}
	err = cmd.Run()
	return out.Bytes(), errb.Bytes(), err
}

// ---------------------------------------------------------------- macOS

// MacKeychain stores generic passwords in the login keychain via the
// `security` CLI. Secrets are written through `security -i` on stdin as
// hex (-X), so they never appear in argv (visible to `ps`) or in errors.
type MacKeychain struct{ bin string }

func (*MacKeychain) Name() string { return "macos-keychain" }

func (k *MacKeychain) Get(ctx context.Context, id string) (string, error) {
	svc, acct, err := splitKeychainID(id)
	if err != nil {
		return "", err
	}
	out, _, err := run(ctx, k.bin, nil, "find-generic-password", "-s", svc, "-a", acct, "-w")
	if code := exitCode(err); code == 44 {
		return "", ErrNotFound
	} else if err != nil {
		return "", fmt.Errorf("credentials: keychain read failed (exit %d)", code)
	}
	v := strings.TrimSuffix(string(out), "\n")
	if v == "" {
		return "", ErrNotFound
	}
	return v, nil
}

func (k *MacKeychain) Set(ctx context.Context, id, value string) error {
	svc, acct, err := splitKeychainID(id)
	if err != nil {
		return err
	}
	line := "add-generic-password -U -s " + svc + " -a " + acct + " -X " + hex.EncodeToString([]byte(value)) + "\n"
	_, stderr, err := run(ctx, k.bin, []byte(line), "-i")
	if err != nil || len(bytes.TrimSpace(stderr)) > 0 {
		// stderr may echo the command (and thus the hex secret): drop it.
		return fmt.Errorf("credentials: keychain write failed (exit %d)", exitCode(err))
	}
	return nil
}

func (k *MacKeychain) Delete(ctx context.Context, id string) error {
	svc, acct, err := splitKeychainID(id)
	if err != nil {
		return err
	}
	_, _, err = run(ctx, k.bin, nil, "delete-generic-password", "-s", svc, "-a", acct)
	if code := exitCode(err); code == 44 {
		return nil
	} else if err != nil {
		return fmt.Errorf("credentials: keychain delete failed (exit %d)", code)
	}
	return nil
}

// ---------------------------------------------------------------- libsecret

// SecretTool uses libsecret through `secret-tool`; the secret goes on stdin.
type SecretTool struct{ bin string }

func (*SecretTool) Name() string { return "libsecret" }

func (s *SecretTool) Get(ctx context.Context, id string) (string, error) {
	svc, acct, err := splitKeychainID(id)
	if err != nil {
		return "", err
	}
	out, _, err := run(ctx, s.bin, nil, "lookup", "service", svc, "account", acct)
	if err != nil {
		if exitCode(err) == 1 && len(out) == 0 {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("credentials: libsecret read failed (exit %d)", exitCode(err))
	}
	if len(out) == 0 {
		return "", ErrNotFound
	}
	return string(out), nil
}

func (s *SecretTool) Set(ctx context.Context, id, value string) error {
	svc, acct, err := splitKeychainID(id)
	if err != nil {
		return err
	}
	_, _, err = run(ctx, s.bin, []byte(value), "store", "--label=crewbase "+svc+"/"+acct, "service", svc, "account", acct)
	if err != nil {
		return fmt.Errorf("credentials: libsecret write failed (exit %d)", exitCode(err))
	}
	return nil
}

func (s *SecretTool) Delete(ctx context.Context, id string) error {
	svc, acct, err := splitKeychainID(id)
	if err != nil {
		return err
	}
	if _, _, err = run(ctx, s.bin, nil, "clear", "service", svc, "account", acct); err != nil && exitCode(err) != 1 {
		return fmt.Errorf("credentials: libsecret delete failed (exit %d)", exitCode(err))
	}
	return nil
}

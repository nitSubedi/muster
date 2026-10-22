// Package service installs the agent to run at login as a per-user
// service: a launchd LaunchAgent on macOS, a systemd user unit on Linux.
// Neither needs root.
package service

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Label identifies the service to launchd and systemd.
const Label = "dev.muster.agent"

// Spec describes what to run.
type Spec struct {
	Exe  string   // absolute path to the muster binary
	Args []string // arguments after "agent"
	Log  string   // macOS only: combined stdout/stderr log file
}

// Plist renders the launchd LaunchAgent for spec.
func Plist(s Spec) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>` + Label + `</string>
	<key>ProgramArguments</key>
	<array>
`)
	for _, a := range append([]string{s.Exe, "agent"}, s.Args...) {
		b.WriteString("\t\t<string>" + xmlEscape(a) + "</string>\n")
	}
	b.WriteString(`	</array>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>ThrottleInterval</key><integer>10</integer>
	<key>StandardOutPath</key><string>` + xmlEscape(s.Log) + `</string>
	<key>StandardErrorPath</key><string>` + xmlEscape(s.Log) + `</string>
</dict>
</plist>
`)
	return b.String()
}

// Unit renders the systemd user unit for spec.
func Unit(s Spec) string {
	parts := []string{systemdQuote(s.Exe), "agent"}
	for _, a := range s.Args {
		parts = append(parts, systemdQuote(a))
	}
	return `[Unit]
Description=Muster agent (every machine you own, one AI endpoint)
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=` + strings.Join(parts, " ") + `
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`
}

func xmlEscape(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// systemdQuote quotes an ExecStart argument. systemd also expands % and $,
// so those are escaped too.
func systemdQuote(s string) string {
	s = strings.ReplaceAll(s, "%", "%%")
	s = strings.ReplaceAll(s, "$", "$$")
	if s != "" && !strings.ContainsAny(s, " \t\"'\\;") {
		return s
	}
	return strconv.Quote(s)
}

// Manager installs and removes the service on this OS.
type Manager struct {
	Home string
	UID  int
	OS   string
	// Run executes a command; replaced in tests.
	Run func(name string, args ...string) ([]byte, error)
}

// NewManager returns a Manager for the current user and OS.
func NewManager() (*Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Manager{Home: home, UID: os.Getuid(), OS: runtime.GOOS, Run: func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).CombinedOutput()
	}}, nil
}

// ErrUnsupported is returned on platforms without a user service manager
// integration yet.
var ErrUnsupported = errors.New("muster service is supported on macOS and Linux; on Windows, add `muster agent` to Task Scheduler (At log on)")

// Path is where the service definition lives.
func (m *Manager) Path() (string, error) {
	switch m.OS {
	case "darwin":
		return filepath.Join(m.Home, "Library", "LaunchAgents", Label+".plist"), nil
	case "linux":
		return filepath.Join(m.Home, ".config", "systemd", "user", "muster.service"), nil
	}
	return "", ErrUnsupported
}

// LogHint tells the user where output goes.
func (m *Manager) LogHint() string {
	if m.OS == "darwin" {
		return m.logPath()
	}
	return "journalctl --user -u muster -f"
}

func (m *Manager) logPath() string { return filepath.Join(m.Home, "Library", "Logs", "muster.log") }

// Install writes the definition and starts the service, replacing any
// previous installation.
func (m *Manager) Install(s Spec) error {
	path, err := m.Path()
	if err != nil {
		return err
	}
	// Only macOS/Linux reach here, so check for a Unix absolute path
	// explicitly rather than with the host's filepath rules.
	if !strings.HasPrefix(s.Exe, "/") {
		return fmt.Errorf("binary path %q must be absolute", s.Exe)
	}
	for _, a := range append([]string{s.Exe}, s.Args...) {
		if strings.IndexFunc(a, unicode.IsControl) >= 0 {
			// A newline would start a new directive in the systemd unit.
			return fmt.Errorf("argument %q contains a control character", a)
		}
	}
	_ = m.Uninstall() // idempotent reinstall
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	switch m.OS {
	case "darwin":
		s.Log = m.logPath()
		if err := os.WriteFile(path, []byte(Plist(s)), 0o644); err != nil {
			return err
		}
		// bootout is asynchronous; bootstrap can briefly fail with
		// "Bootstrap failed: 5" right after a reinstall.
		var err error
		for i := 0; i < 5; i++ {
			if err = m.run("launchctl", "bootstrap", m.domain(), path); err == nil {
				return nil
			}
			time.Sleep(300 * time.Millisecond)
		}
		return err
	default:
		if err := os.WriteFile(path, []byte(Unit(s)), 0o644); err != nil {
			return err
		}
		if err := m.run("systemctl", "--user", "daemon-reload"); err != nil {
			return err
		}
		return m.run("systemctl", "--user", "enable", "--now", "muster.service")
	}
}

// Uninstall stops the service and removes its definition. Removing a
// service that isn't installed is not an error.
func (m *Manager) Uninstall() error {
	path, err := m.Path()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	switch m.OS {
	case "darwin":
		m.run("launchctl", "bootout", m.domain()+"/"+Label)
	default:
		m.run("systemctl", "--user", "disable", "--now", "muster.service")
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	if m.OS == "linux" {
		m.run("systemctl", "--user", "daemon-reload")
	}
	return nil
}

// Status returns the service manager's view, for `muster service status`.
func (m *Manager) Status() (string, error) {
	if _, err := m.Path(); err != nil {
		return "", err
	}
	var out []byte
	var err error
	if m.OS == "darwin" {
		out, err = m.Run("launchctl", "print", m.domain()+"/"+Label)
	} else {
		out, err = m.Run("systemctl", "--user", "status", "--no-pager", "muster.service")
	}
	if err != nil && len(out) == 0 {
		return "not installed", nil
	}
	return string(out), nil
}

func (m *Manager) domain() string { return "gui/" + strconv.Itoa(m.UID) }

func (m *Manager) run(name string, args ...string) error {
	out, err := m.Run(name, args...)
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

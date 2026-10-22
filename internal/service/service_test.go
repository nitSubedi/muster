package service

import (
	"encoding/xml"
	"os"
	"strings"
	"testing"
)

func TestPlistIsValidXMLAndEscapes(t *testing.T) {
	p := Plist(Spec{Exe: "/Users/a b/bin/muster", Args: []string{"--name", "R&D <box>"}, Log: "/tmp/m.log"})
	if err := xml.Unmarshal([]byte(p), new(struct{ XMLName xml.Name })); err != nil {
		t.Fatalf("invalid XML: %v\n%s", err, p)
	}
	for _, want := range []string{"<string>/Users/a b/bin/muster</string>", "<string>agent</string>", "R&amp;D &lt;box&gt;", "<key>KeepAlive</key><true/>"} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q", want)
		}
	}
}

func TestUnitQuotesArgs(t *testing.T) {
	u := Unit(Spec{Exe: "/home/m/bin/muster", Args: []string{"--name", "my box", "--peer", "a:7878", "--x", "50%$HOME"}})
	want := `ExecStart=/home/m/bin/muster agent --name "my box" --peer a:7878 --x 50%%$$HOME`
	if !strings.Contains(u, want) {
		t.Fatalf("unit:\n%s\nwant line %s", u, want)
	}
	if !strings.Contains(u, "WantedBy=default.target") || !strings.Contains(u, "Restart=on-failure") {
		t.Fatal("unit missing install/restart sections")
	}
}

type call []string

func fakeManager(t *testing.T, osName string) (*Manager, *[]call) {
	var calls []call
	return &Manager{Home: t.TempDir(), UID: 501, OS: osName, Run: func(name string, args ...string) ([]byte, error) {
		calls = append(calls, append(call{name}, args...))
		return nil, nil
	}}, &calls
}

func joined(cs []call) string {
	var s []string
	for _, c := range cs {
		s = append(s, strings.Join(c, " "))
	}
	return strings.Join(s, "\n")
}

func TestInstallUninstallLinux(t *testing.T) {
	m, calls := fakeManager(t, "linux")
	if err := m.Install(Spec{Exe: "/usr/local/bin/muster"}); err != nil {
		t.Fatal(err)
	}
	path, _ := m.Path()
	if b, err := os.ReadFile(path); err != nil || !strings.Contains(string(b), "ExecStart=/usr/local/bin/muster agent") {
		t.Fatalf("unit not written: %v %s", err, b)
	}
	if got := joined(*calls); !strings.Contains(got, "systemctl --user enable --now muster.service") {
		t.Fatalf("calls:\n%s", got)
	}

	*calls = nil
	if err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("unit not removed")
	}
	if got := joined(*calls); !strings.Contains(got, "systemctl --user disable --now muster.service") {
		t.Fatalf("calls:\n%s", got)
	}
}

func TestInstallMacUsesGUIDomainAndReplaces(t *testing.T) {
	m, calls := fakeManager(t, "darwin")
	m.Install(Spec{Exe: "/opt/muster"})
	*calls = nil
	if err := m.Install(Spec{Exe: "/opt/muster", Args: []string{"--name", "new"}}); err != nil {
		t.Fatal(err)
	}
	got := joined(*calls)
	if !strings.Contains(got, "launchctl bootout gui/501/"+Label) || !strings.Contains(got, "launchctl bootstrap gui/501 ") {
		t.Fatalf("reinstall should bootout then bootstrap:\n%s", got)
	}
	path, _ := m.Path()
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "<string>new</string>") {
		t.Fatal("plist not replaced")
	}
}

func TestUninstallWhenAbsentIsNoop(t *testing.T) {
	m, calls := fakeManager(t, "linux")
	if err := m.Uninstall(); err != nil || len(*calls) != 0 {
		t.Fatalf("err=%v calls=%v", err, *calls)
	}
}

func TestRejectsRelativeExeAndWindows(t *testing.T) {
	m, _ := fakeManager(t, "linux")
	if err := m.Install(Spec{Exe: "bin/muster"}); err == nil {
		t.Fatal("relative path accepted")
	}
	w, _ := fakeManager(t, "windows")
	if err := w.Install(Spec{Exe: `C:\muster.exe`}); err != ErrUnsupported {
		t.Fatalf("err = %v", err)
	}
}

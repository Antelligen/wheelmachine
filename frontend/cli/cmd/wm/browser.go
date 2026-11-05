package main

import (
	"errors"
	"net"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
)

// openBrowser opens an address of the API's own in the person's browser: only a web address (https, or http on this
// computer), never a file or another program's link, whatever the answer said. On a computer with no screen it does
// nothing - a browser of the terminal's own would take the terminal over - and the address is shown to open elsewhere.
func (a *app) openBrowser(address string) error {
	u, err := url.Parse(address)
	if err != nil || u.Host == "" || u.User != nil {
		return errors.New("not a web address")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if ip := net.ParseIP(u.Hostname()); !strings.EqualFold(u.Hostname(), "localhost") && (ip == nil || !ip.IsLoopback()) {
			return errors.New("not an https address")
		}
	default:
		return errors.New("not a web address")
	}
	if a.open != nil {
		return a.open(u.String())
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u.String())
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u.String())
	default:
		if a.getenv("DISPLAY") == "" && a.getenv("WAYLAND_DISPLAY") == "" {
			return errors.New("no screen here")
		}
		cmd = exec.Command("xdg-open", u.String())
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

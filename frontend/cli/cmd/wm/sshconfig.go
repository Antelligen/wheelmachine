package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"wheelmachine/frontend/cli/api"
)

// wm ssh-config <machine> signs a short-lived certificate for your SSH key and prints the ~/.ssh/config block that
// lets your own ssh, scp and sftp reach the machine directly - no ProxyCommand. It writes the certificate and the
// server's host key beside wm's own files and points the block at them; the certificate lasts an hour, so re-run it
// (or `ssh` will say the certificate expired) to refresh.
func (a *app) sshConfig(ctx context.Context, args []string) error {
	fs := a.flags("ssh-config", "wm ssh-config <machine> [--identity ~/.ssh/id_ed25519.pub]",
		"Sign a certificate for your SSH key and print the ~/.ssh/config block to reach a machine with your own ssh, scp and sftp.")
	identity := fs.String("identity", "", "your SSH public key `file`; ~/.ssh/id_ed25519.pub when left out")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, 1, "which machine"); err != nil {
		return err
	}
	c, id, err := a.machine(ctx, words[0])
	if err != nil {
		return err
	}
	pubPath := *identity
	if pubPath == "" {
		pubPath = filepath.Join(a.home(), ".ssh", "id_ed25519.pub")
	}
	pub, err := os.ReadFile(pubPath)
	if err != nil {
		return usagef("your SSH public key was not found at %s: make a key (ssh-keygen -t ed25519) or give --identity <file.pub>", pubPath)
	}
	cert, err := c.SignSSHCertificate(ctx, api.SSHCertificateRequest{PublicKey: strings.TrimSpace(string(pub)), Machine: id})
	if err != nil {
		return err
	}
	if cert.SSHHost == "" {
		return fmt.Errorf("the SSH server's address is not set up yet; the certificate was signed but there is nowhere to point ssh at")
	}
	host, port := splitHostPort(cert.SSHHost)

	dir := filepath.Join(a.configDir(), "ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	certPath := filepath.Join(dir, id+"-cert.pub")
	if err := os.WriteFile(certPath, []byte(cert.Certificate+"\n"), 0o644); err != nil {
		return err
	}
	knownHosts := filepath.Join(dir, "known_hosts")
	if cert.HostPublicKey != "" {
		line := fmt.Sprintf("[%s]:%s %s\n", host, port, cert.HostPublicKey)
		if err := os.WriteFile(knownHosts, []byte(line), 0o644); err != nil {
			return err
		}
	}
	// the private key beside the public one the certificate is for
	idFile := strings.TrimSuffix(pubPath, ".pub")

	var b strings.Builder
	fmt.Fprintf(&b, "# Wheelmachine: %s. The certificate lasts an hour; run wm ssh-config %s again to refresh it.\n", id, words[0])
	fmt.Fprintf(&b, "Host %s\n", id)
	fmt.Fprintf(&b, "    HostName %s\n", host)
	fmt.Fprintf(&b, "    Port %s\n", port)
	fmt.Fprintf(&b, "    User %s\n", cert.SSHUser)
	fmt.Fprintf(&b, "    IdentityFile %s\n", idFile)
	fmt.Fprintf(&b, "    CertificateFile %s\n", certPath)
	fmt.Fprintf(&b, "    IdentitiesOnly yes\n")
	if cert.HostPublicKey != "" {
		fmt.Fprintf(&b, "    UserKnownHostsFile %s\n", knownHosts)
	}
	fmt.Fprint(a.stdout, b.String())
	if !a.quiet {
		fmt.Fprintf(a.stderr, "wm: add this to ~/.ssh/config, then: ssh %s   (scp ./f %s:, sftp %s)\n", id, id, id)
	}
	return nil
}

// splitHostPort splits "host:port", defaulting the port to 2222 (the SSH server's own).
func splitHostPort(addr string) (host, port string) {
	if i := strings.LastIndex(addr, ":"); i >= 0 && !strings.Contains(addr[i:], "]") {
		return addr[:i], addr[i+1:]
	}
	return addr, "2222"
}

func (a *app) home() string {
	if h := a.getenv("HOME"); h != "" {
		return h
	}
	if h := a.getenv("USERPROFILE"); h != "" {
		return h
	}
	return "."
}

// configDir is where wm keeps its files: $XDG_CONFIG_HOME/wheelmachine, else ~/.config/wheelmachine.
func (a *app) configDir() string {
	if x := a.getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "wheelmachine")
	}
	return filepath.Join(a.home(), ".config", "wheelmachine")
}

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHConfigSignsACertAndPrintsADirectBlock(t *testing.T) {
	h := newHarness(t)
	m := h.api.AddMachine("box", "running")
	// a public key where ~/.ssh/id_ed25519.pub would be
	home := h.env["HOME"]
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	pub := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAISOMEKEY me@laptop"
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519.pub"), []byte(pub+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := h.wm("", "ssh-config", m.ID).must(t, 0)

	if h.api.SSHSignedFor() != pub {
		t.Fatalf("the certificate was signed for %q, want %q", h.api.SSHSignedFor(), pub)
	}
	// a direct block, no ProxyCommand
	for _, want := range []string{"Host " + m.ID, "HostName ssh.wheelmachine.ai", "Port 2222", "User " + m.ID,
		"CertificateFile ", "IdentityFile " + filepath.Join(home, ".ssh", "id_ed25519"), "IdentitiesOnly yes"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("the block lacks %q:\n%s", want, r.stdout)
		}
	}
	if strings.Contains(r.stdout, "ProxyCommand") {
		t.Fatalf("the block uses a ProxyCommand:\n%s", r.stdout)
	}
	// the certificate and host key were written for ssh to read
	certPath := filepath.Join(h.env["XDG_CONFIG_HOME"], "wheelmachine", "ssh", m.ID+"-cert.pub")
	if b, err := os.ReadFile(certPath); err != nil || !strings.Contains(string(b), "cert-v01") {
		t.Fatalf("certificate file %s: %q %v", certPath, b, err)
	}
	known := filepath.Join(h.env["XDG_CONFIG_HOME"], "wheelmachine", "ssh", "known_hosts")
	if b, err := os.ReadFile(known); err != nil || !strings.Contains(string(b), "[ssh.wheelmachine.ai]:2222") {
		t.Fatalf("known_hosts %s: %q %v", known, b, err)
	}
}

func TestSSHConfigSaysWhenThereIsNoKey(t *testing.T) {
	h := newHarness(t)
	m := h.api.AddMachine("box", "running")
	r := h.wm("", "ssh-config", m.ID).must(t, 2)
	if !strings.Contains(r.stderr, "SSH public key was not found") {
		t.Fatalf("stderr: %q", r.stderr)
	}
}

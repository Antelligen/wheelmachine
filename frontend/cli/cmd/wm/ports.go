package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"wheelmachine/frontend/cli/api"
)

var portCommands = map[string]subcommand{
	"ls":    (*app).portsList,
	"open":  (*app).portsOpen,
	"close": (*app).portsClose,
	"link":  (*app).portsLink,
}

// portOf reads a port as typed: a number from 1 to 65535.
func portOf(word string) (int, error) {
	n, err := strconv.Atoi(word)
	if err != nil || n < 1 || n > 65535 {
		return 0, usagef("a port is a number from 1 to 65535, not %q", word)
	}
	return n, nil
}

// access is who can reach a port's address.
func access(p api.Port) string {
	if p.Public {
		return "public"
	}
	return "private"
}

func (a *app) portsList(ctx context.Context, args []string) error {
	fs := a.flags("ports ls", "wm ports ls <machine>", "List a machine's ports open at web addresses, lowest first, and who "+
		"can reach each: private (an API key that can see the machine, or a link from wm ports link) or public (anyone).")
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
	ports, err := c.ListPorts(ctx, id)
	if err != nil {
		return err
	}
	switch {
	case a.json:
		if ports == nil {
			ports = []api.Port{}
		}
		return a.printJSON(ports)
	case a.quiet:
		for _, p := range ports {
			fmt.Fprintln(a.stdout, p.Port)
		}
		return nil
	}
	t := a.table("PORT", "ACCESS", "ADDRESS")
	for _, p := range ports {
		t.row(strconv.Itoa(p.Port), access(p), p.URL)
	}
	return t.flush()
}

func (a *app) portsOpen(ctx context.Context, args []string) error {
	fs := a.flags("ports open", "wm ports open <machine> <port> [--public]", "Open a port of a machine at a web address of "+
		"its own, for a program listening on it, and print the address. It is private unless --public: it answers a call "+
		"with an API key that can see the machine, and a browser that opened a link from wm ports link. A port open "+
		"already is made public or private as asked.")
	public := fs.Bool("public", false, "let anyone reach it, with no key or link")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 2, 2, "which machine, and which port"); err != nil {
		return err
	}
	port, err := portOf(words[1])
	if err != nil {
		return err
	}
	c, id, err := a.machine(ctx, words[0])
	if err != nil {
		return err
	}
	p, err := c.OpenPort(ctx, id, port, *public)
	if err != nil {
		return err
	}
	if a.json {
		err = a.printJSON(p)
	} else {
		_, err = fmt.Fprintln(a.stdout, p.URL)
	}
	if err != nil || a.quiet {
		return err
	}
	if p.Public {
		fmt.Fprintln(a.stderr, "wm: the address is public: anyone can reach it, with no key or link.")
	} else {
		fmt.Fprintf(a.stderr, "wm: the address is private: it answers a call with an API key that can see the machine, or a "+
			"browser that opened a link from wm ports link %s %d.\n", words[0], p.Port)
	}
	return nil
}

func (a *app) portsClose(ctx context.Context, args []string) error {
	fs := a.flags("ports close", "wm ports close <machine> <port>", "Close a port's web address: it answers 404 from then on, "+
		"and links made for it no longer open it. A port that is not open is closed all the same.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 2, 2, "which machine, and which port"); err != nil {
		return err
	}
	port, err := portOf(words[1])
	if err != nil {
		return err
	}
	c, id, err := a.machine(ctx, words[0])
	if err != nil {
		return err
	}
	if err := c.ClosePort(ctx, id, port); err != nil {
		return err
	}
	fmt.Fprintln(a.stdout, port)
	return nil
}

func (a *app) portsLink(ctx context.Context, args []string) error {
	fs := a.flags("ports link", "wm ports link <machine> <port> [--for 30m] [--path /]", "Make a link that lets one browser "+
		"into a port's private address, and print it. It opens once, within five minutes: the browser that opens it lands "+
		"on --path, and may reach that address alone for as long as --for says. Each call makes a new link, shown this once.")
	keep := fs.Duration("for", 30*time.Minute, "how long the browser that opens it may reach the address, 1m to 60m")
	path := fs.String("path", "/", "where on the address it lands, a `path` beginning with / (a query may follow)")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 2, 2, "which machine, and which port"); err != nil {
		return err
	}
	port, err := portOf(words[1])
	if err != nil {
		return err
	}
	if *keep%time.Second != 0 || *keep < time.Minute || *keep > time.Hour {
		return usagef("--for is whole seconds from 1m to 60m")
	}
	if !strings.HasPrefix(*path, "/") {
		return usagef("--path begins with /, not %q", *path)
	}
	c, id, err := a.machine(ctx, words[0])
	if err != nil {
		return err
	}
	l, err := c.LinkPort(ctx, id, port, api.PortLinkCreate{AccessS: int(*keep / time.Second), Path: *path})
	if err != nil {
		return err
	}
	if a.json {
		err = a.printJSON(l)
	} else {
		_, err = fmt.Fprintln(a.stdout, l.URL)
	}
	if err != nil || a.quiet {
		return err
	}
	fmt.Fprintf(a.stderr, "wm: the link opens once, within five minutes (until %s), and gives the browser that opens it "+
		"access to the address for %s.\n", l.ExpiresAt.UTC().Format("15:04:05 UTC"), span(float64(l.AccessS)))
	return nil
}

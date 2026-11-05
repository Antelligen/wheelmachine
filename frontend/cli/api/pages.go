package api

import (
	"context"
	"fmt"
	"iter"
	"strings"
)

// all walks a list page by page from `start`, fetching a page only when the one before it is used up. A page with
// nothing on it but a `next` is passed over (a list narrowed by state can have such pages); a `next` seen before
// ends the walk, so a list that goes round never runs for ever.
func all[T any](start string, fetch func(after string) (*Page[T], error)) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		after := start
		seen := map[string]bool{}
		for {
			page, err := fetch(after)
			if err != nil {
				var zero T
				yield(zero, err)
				return
			}
			for _, v := range page.Data {
				if !yield(v, nil) {
					return
				}
			}
			next := page.After()
			if next == "" || seen[next] {
				return
			}
			seen[next] = true
			after = next
		}
	}
}

// IsMachineID reports whether s has the shape of a machine's id: m_ and 26 lower-case letters and digits.
func IsMachineID(s string) bool { return hasIDShape(s, "m_") }

// IsProjectID reports whether s has the shape of a project's id: prj_ and 26 lower-case letters and digits.
func IsProjectID(s string) bool { return hasIDShape(s, "prj_") }

// IsAccountID reports whether s has the shape of an account's id, an organisation's among them: acc_ and 26
// lower-case letters and digits.
func IsAccountID(s string) bool { return hasIDShape(s, "acc_") }

// IsKeyID reports whether s has the shape of a key's id: key_ and 16 hexadecimal digits.
func IsKeyID(s string) bool {
	if len(s) != 20 || !strings.HasPrefix(s, "key_") {
		return false
	}
	for _, c := range s[4:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func hasIDShape(s, prefix string) bool {
	if len(s) != len(prefix)+26 || !strings.HasPrefix(s, prefix) {
		return false
	}
	for _, c := range s[len(prefix):] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return true
}

// FindMachine gives the id of the machine named by idOrName: an id as it is, else the one machine of that name.
func (c *Client) FindMachine(ctx context.Context, idOrName string) (string, error) {
	if IsMachineID(idOrName) {
		return idOrName, nil
	}
	return find("machine", idOrName, func(yield func(string, string) bool) error {
		for m, err := range c.AllMachines(ctx, ListMachinesParams{Limit: 200}) {
			if err != nil {
				return err
			}
			if !yield(m.ID, m.Name) {
				return nil
			}
		}
		return nil
	})
}

// FindProject gives the id of the project named by idOrName: an id as it is, else the one project of that name.
func (c *Client) FindProject(ctx context.Context, idOrName string) (string, error) {
	if IsProjectID(idOrName) {
		return idOrName, nil
	}
	return find("project", idOrName, func(yield func(string, string) bool) error {
		for p, err := range c.AllProjects(ctx, ListParams{Limit: 200}) {
			if err != nil {
				return err
			}
			if !yield(p.ID, p.Name) {
				return nil
			}
		}
		return nil
	})
}

// FindKey gives the id of the key named by idOrName: an id as it is, else the one key of that name.
func (c *Client) FindKey(ctx context.Context, idOrName string) (string, error) {
	if IsKeyID(idOrName) {
		return idOrName, nil
	}
	return find("key", idOrName, func(yield func(string, string) bool) error {
		keys, err := c.ListKeys(ctx)
		if err != nil {
			return err
		}
		for _, k := range keys {
			if !yield(k.ID, k.Name) {
				return nil
			}
		}
		return nil
	})
}

// find looks through (id, name) pairs for the one with this name.
func find(what, name string, each func(yield func(id, name string) bool) error) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", &Error{Code: CodeInvalidRequest, Detail: "say which " + what + ": its id or its name"}
	}
	var found []string
	err := each(func(id, n string) bool {
		if n == name {
			found = append(found, id)
		}
		return len(found) < 2
	})
	if err != nil {
		return "", err
	}
	switch len(found) {
	case 0:
		return "", &Error{Status: 404, Code: CodeNotFound, Detail: fmt.Sprintf("no %s has the id or name %q", what, name)}
	case 1:
		return found[0], nil
	}
	return "", &Error{Status: 409, Code: CodeConflict, Detail: fmt.Sprintf("more than one %s is named %q: give its id", what, name)}
}

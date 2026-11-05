package api

import (
	"context"
	"fmt"
	"net/http"
)

// SecretValueMax is the most a secret's value holds.
const SecretValueMax = 32 << 10

// ValidSecretName says whether a name is a secret's: capitals, digits and _, not beginning with a digit, at most 64 -
// an environment variable's name, which is what a machine's commands get it as.
func ValidSecretName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i, c := range name {
		if !(c == '_' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func secretName(name string) (string, error) {
	if !ValidSecretName(name) {
		return "", &Error{Code: CodeInvalidRequest, Detail: fmt.Sprintf("%q is not a secret's name: capitals, digits and _, such as DATABASE_URL", name)}
	}
	return name, nil
}

// SetSecret sets a secret, or sets it anew, as the API keeps one of its name by default: an AI key (a name of
// SecretPresets) outside the machines and on every one of them, any other an ordinary secret. The machines that get it
// get the value from their next command. The value goes in the request's body alone, and the API never shows it
// again: the answer is the secret without it.
func (c *Client) SetSecret(ctx context.Context, name, value string) (*Secret, error) {
	return c.SetSecretAs(ctx, name, value, SecretSet{})
}

// SetSecretAs sets a secret as `how` says it is kept: inside the machines, outside them by a policy of its own, or
// only on the machines given it. Said whole: what is left out is the API's default for the name, whatever it was.
func (c *Client) SetSecretAs(ctx context.Context, name, value string, how SecretSet) (*Secret, error) {
	n, err := secretName(name)
	if err != nil {
		return nil, err
	}
	if how.Inside && how.Outside != nil {
		return nil, &Error{Code: CodeInvalidRequest, Detail: "a secret is kept inside the machines or outside them, not both"}
	}
	body := struct {
		Value        string `json:"value"`
		Outside      any    `json:"outside,omitempty"`
		EveryMachine *bool  `json:"every_machine,omitempty"`
	}{Value: value}
	switch {
	case how.Inside:
		body.Outside = false
	case how.Outside != nil:
		body.Outside = how.Outside
	}
	if how.OnlyGiven {
		no := false
		body.EveryMachine = &no
	}
	var out Secret
	if err := c.decode(ctx, call{method: http.MethodPut, path: "/v1/secrets/" + n, in: body, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SecretPresets is the AI keys the API knows by their names, in the order agents choose between them: for each, the
// names it is set under and the hosts its key is sent to.
func (c *Client) SecretPresets(ctx context.Context) ([]SecretPreset, error) {
	var out Page[SecretPreset]
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/secrets/presets", timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// Secrets is the account's secrets by name, each with the machines given it; never a value.
func (c *Client) Secrets(ctx context.Context) ([]Secret, error) {
	var out Page[Secret]
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/secrets", timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// DeleteSecret deletes a secret and takes it off every machine given it.
func (c *Client) DeleteSecret(ctx context.Context, name string) error {
	n, err := secretName(name)
	if err != nil {
		return err
	}
	return c.decode(ctx, call{method: http.MethodDelete, path: "/v1/secrets/" + n, timeout: callTimeout}, nil)
}

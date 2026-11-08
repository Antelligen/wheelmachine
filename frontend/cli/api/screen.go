package api

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
)

// ScreenPicture is a picture of a machine's desktop, as the API answers it: its size in pixels, how far it was drawn down,
// and its file as base64. A picture shows whatever is on the screen.
type ScreenPicture struct {
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	Scale      float64 `json:"scale"`
	Format     string  `json:"format"`
	DataBase64 string  `json:"data_base64"`
}

// Bytes is the picture's file.
func (p *ScreenPicture) Bytes() ([]byte, error) { return base64.StdEncoding.DecodeString(p.DataBase64) }

// ScreenRegion is a part of the screen, in pixels of a picture at the scale asked for. Left at zero, W and H run to the
// screen's edge.
type ScreenRegion struct {
	X int `json:"x,omitempty"`
	Y int `json:"y,omitempty"`
	W int `json:"w,omitempty"`
	H int `json:"h,omitempty"`
}

// ScreenPictureParams says what picture to take: 0 for the API's scale of 0.5 (1, or one over a whole number up to 8), png or
// jpeg, "" for png, and a part of the screen, nil for all of it.
type ScreenPictureParams struct {
	Scale  float64
	Format string
	Region *ScreenRegion
}

// TakeScreenPicture sees a machine's desktop as a picture. A paused machine is woken for it, and its desktop turned on,
// which can take up to a minute the first time.
func (c *Client) TakeScreenPicture(ctx context.Context, machine string, p ScreenPictureParams) (*ScreenPicture, error) {
	id, err := segment("machine", machine)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	if p.Scale != 0 {
		q.Set("scale", strconv.FormatFloat(p.Scale, 'f', -1, 64))
	}
	if p.Format != "" {
		q.Set("format", p.Format)
	}
	if r := p.Region; r != nil {
		if r.X != 0 {
			q.Set("x", strconv.Itoa(r.X))
		}
		if r.Y != 0 {
			q.Set("y", strconv.Itoa(r.Y))
		}
		if r.W != 0 {
			q.Set("w", strconv.Itoa(r.W))
		}
		if r.H != 0 {
			q.Set("h", strconv.Itoa(r.H))
		}
	}
	var out ScreenPicture
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/machines/" + id + "/screen/picture", query: q, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ScreenAction is one thing to do on a machine's desktop. Type says which: click (X, Y, Button, Count), move (X, Y), drag (X,
// Y, ToX, ToY, Button), scroll (X, Y, DX, DY), keys (Keys, pressed together), type (Text) or wait (MS). Places are pixels of a
// picture of the whole screen at the scale of the call.
type ScreenAction struct {
	Type   string   `json:"type"`
	X      *int     `json:"x,omitempty"`
	Y      *int     `json:"y,omitempty"`
	ToX    *int     `json:"to_x,omitempty"`
	ToY    *int     `json:"to_y,omitempty"`
	Button string   `json:"button,omitempty"`
	Count  *int     `json:"count,omitempty"`
	DX     *int     `json:"dx,omitempty"`
	DY     *int     `json:"dy,omitempty"`
	Keys   []string `json:"keys,omitempty"`
	Text   *string  `json:"text,omitempty"`
	MS     *int     `json:"ms,omitempty"`
}

// ScreenActions is what to do in one call, in order: 1 to 64 actions, the scale their places are given at (nil: 0.5), whether
// to answer with a picture once they are done, and how long to wait before it (nil: half a second).
type ScreenActions struct {
	Actions  []ScreenAction `json:"actions"`
	Scale    *float64       `json:"scale,omitempty"`
	Picture  bool           `json:"picture,omitempty"`
	SettleMS *int           `json:"settle_ms,omitempty"`
}

// ScreenActionsDone is how many actions were done, all of them, and the picture when one was asked for.
type ScreenActionsDone struct {
	Done    int            `json:"done"`
	Picture *ScreenPicture `json:"picture,omitempty"`
}

// WorkScreen works a machine's desktop: the actions in order, the machine woken and its desktop turned on as for a picture. A
// click or a word must not happen twice, so a call that may have reached the API is never sent again, not even when it is
// refused for a moment: it is the caller's to send again, once it has looked at the screen.
func (c *Client) WorkScreen(ctx context.Context, machine string, a ScreenActions) (*ScreenActionsDone, error) {
	id, err := segment("machine", machine)
	if err != nil {
		return nil, err
	}
	var out ScreenActionsDone
	if err := c.decode(ctx, call{method: http.MethodPost, path: "/v1/machines/" + id + "/screen/actions", in: a, once: true, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

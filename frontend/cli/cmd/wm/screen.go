package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"wheelmachine/frontend/cli/api"
)

// A flag for the scale places and pictures are given at: 0 leaves it to the API, whose scale is 0.5.
const scaleUsage = "how far a picture is drawn down, and the scale `x` and `y` are given at: 1, or one over a whole number up to 8 (0.5 when " +
	"left out: 1440 x 900)"

func (a *app) machinesScreenshot(ctx context.Context, args []string) error {
	fs := a.flags("machines screenshot", "wm machines screenshot <machine> [<file>] [flags]", "See a machine's desktop as a picture, written to a "+
		"file: screen.png when none is named, and - for standard output. A paused machine is woken for it, and its desktop "+
		"turned on, which can take up to a minute the first time. A picture shows whatever is on the screen, so take care where you send it.")
	scale := fs.Float64("scale", 0, scaleUsage)
	format := fs.String("format", "", "png or jpeg; png unless the file's name says .jpg or .jpeg")
	region := fs.String("region", "", "only this part of the screen, as `x,y,w,h`, in pixels of a picture at this scale")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, 2, "which machine"); err != nil {
		return err
	}
	file := "screen.png"
	if len(words) == 2 {
		file = words[1]
	}
	if *format == "" {
		switch strings.ToLower(filepath.Ext(file)) {
		case ".jpg", ".jpeg":
			*format = "jpeg"
		}
	}
	if *format != "" && *format != "png" && *format != "jpeg" {
		return usagef("--format is png or jpeg")
	}
	if *format == "jpeg" && len(words) == 1 {
		file = "screen.jpg"
	}
	p := api.ScreenPictureParams{Scale: *scale, Format: *format}
	if *region != "" {
		if p.Region, err = regionOf(*region); err != nil {
			return err
		}
	}
	c, id, err := a.machine(ctx, words[0])
	if err != nil {
		return err
	}
	pic, err := c.TakeScreenPicture(ctx, id, p)
	if err != nil {
		return err
	}
	data, err := pic.Bytes()
	if err != nil {
		return fmt.Errorf("the picture could not be read: %w", err)
	}
	if file == "-" {
		_, err := a.stdout.Write(data)
		return err
	}
	// (0600: a picture may show anything that was on the screen)
	if err := os.WriteFile(file, data, 0o600); err != nil {
		return err
	}
	switch {
	case a.json:
		return a.printJSON(map[string]any{"file": file, "width": pic.Width, "height": pic.Height, "scale": pic.Scale, "format": pic.Format})
	case a.quiet:
		fmt.Fprintln(a.stdout, file)
	default:
		fmt.Fprintf(a.stdout, "%s: %d x %d at scale %s\n", file, pic.Width, pic.Height, strconv.FormatFloat(pic.Scale, 'f', -1, 64))
	}
	return nil
}

// A part of the screen as a flag gives it: x,y,w,h.
func regionOf(v string) (*api.ScreenRegion, error) {
	parts := strings.Split(v, ",")
	if len(parts) != 4 {
		return nil, usagef("--region is x,y,w,h")
	}
	var n [4]int
	for i, part := range parts {
		var err error
		if n[i], err = strconv.Atoi(strings.TrimSpace(part)); err != nil || n[i] < 0 || i >= 2 && n[i] == 0 {
			return nil, usagef("--region is x,y,w,h: whole numbers, its width and height above 0")
		}
	}
	return &api.ScreenRegion{X: n[0], Y: n[1], W: n[2], H: n[3]}, nil
}

// One call of actions on a machine's desktop, at this scale, and what is said when it is done.
func (a *app) workScreen(ctx context.Context, machine string, scale float64, actions ...api.ScreenAction) error {
	c, id, err := a.machine(ctx, machine)
	if err != nil {
		return err
	}
	req := api.ScreenActions{Actions: actions}
	if scale != 0 {
		req.Scale = &scale
	}
	done, err := c.WorkScreen(ctx, id, req)
	if err != nil {
		return err
	}
	switch {
	case a.json:
		return a.printJSON(done)
	case !a.quiet:
		fmt.Fprintln(a.stdout, "done")
	}
	return nil
}

// A place on the screen, as the words that say it.
func placeOf(x, y string) (*int, *int, error) {
	px, errX := strconv.Atoi(x)
	py, errY := strconv.Atoi(y)
	if errX != nil || errY != nil || px < 0 || py < 0 {
		return nil, nil, usagef("x and y are whole numbers from 0, in pixels of a picture of the screen at this scale")
	}
	return &px, &py, nil
}

func (a *app) machinesClick(ctx context.Context, args []string) error {
	fs := a.flags("machines click", "wm machines click <machine> <x> <y> [flags]", "Click on a machine's desktop, at a place in pixels of a picture "+
		"of the screen at the same scale (wm machines screenshot).")
	scale := fs.Float64("scale", 0, scaleUsage)
	button := fs.String("button", "", "left, middle or right; left when left out")
	clicks := fs.Int("count", 1, "how many times: 2 for a double click")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 3, 3, "which machine, and where: <x> <y>"); err != nil {
		return err
	}
	x, y, err := placeOf(words[1], words[2])
	if err != nil {
		return err
	}
	act := api.ScreenAction{Type: "click", X: x, Y: y, Button: *button}
	if *clicks != 1 {
		act.Count = clicks
	}
	return a.workScreen(ctx, words[0], *scale, act)
}

func (a *app) machinesType(ctx context.Context, args []string) error {
	fs := a.flags("machines type", "wm machines type <machine> [<text>]", "Type on a machine's desktop, wherever it is taking keys: the text given, "+
		"or standard input with none, its last line's end left off, so that a password stays out of your shell's history. Letters of "+
		"every language and emoji can be typed, but characters that aren't on an English keyboard, such as accents, Russian, Chinese or "+
		"emoji, can come out wrong. Take a picture after typing them to check; a password box can't be checked this way. At most 4096 "+
		"characters. It is never kept or logged.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, 2, "which machine"); err != nil {
		return err
	}
	var text string
	if len(words) == 2 {
		text = words[1]
	} else {
		raw, err := io.ReadAll(io.LimitReader(a.stdin, 1<<20))
		if err != nil {
			return err
		}
		text = strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	}
	if text == "" {
		return usagef("say what to type")
	}
	return a.workScreen(ctx, words[0], 0, api.ScreenAction{Type: "type", Text: &text})
}

func (a *app) machinesKeys(ctx context.Context, args []string) error {
	fs := a.flags("machines keys", "wm machines keys <machine> <keys>...", "Press keys on a machine's desktop: each word its keys together, joined by "+
		"+, such as ctrl+c, and the words one after another, such as ctrl+a delete. A key is a character, or one of enter, tab, escape, "+
		"backspace, delete, insert, home, end, pageup, pagedown, up, down, left, right, space, ctrl, shift, alt, super, menu, capslock, "+
		"printscreen, f1 to f12.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 2, 65, "which machine, and which keys"); err != nil {
		return err
	}
	var actions []api.ScreenAction
	for _, word := range words[1:] {
		keys, err := keysOf(word)
		if err != nil {
			return err
		}
		actions = append(actions, api.ScreenAction{Type: "keys", Keys: keys})
	}
	return a.workScreen(ctx, words[0], 0, actions...)
}

// A word's keys, joined by +: the key + itself is one at the end of a word, as in ctrl++.
func keysOf(word string) ([]string, error) {
	if word == "+" {
		return []string{"+"}, nil
	}
	var keys []string
	if rest, plus := strings.CutSuffix(word, "++"); plus {
		keys = append(strings.Split(rest, "+"), "+")
	} else {
		keys = strings.Split(word, "+")
	}
	for _, k := range keys {
		if k == "" {
			return nil, usagef("keys are joined by +, as in ctrl+c")
		}
	}
	return keys, nil
}

func (a *app) machinesScroll(ctx context.Context, args []string) error {
	fs := a.flags("machines scroll", "wm machines scroll <machine> <x> <y> [flags]", "Scroll on a machine's desktop, at a place in pixels of a picture of "+
		"the screen at the same scale.")
	scale := fs.Float64("scale", 0, scaleUsage)
	dy := fs.Int("dy", 0, "notches down, or up below 0")
	dx := fs.Int("dx", 0, "notches to the right, or to the left below 0")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 3, 3, "which machine, and where: <x> <y>"); err != nil {
		return err
	}
	if *dx == 0 && *dy == 0 {
		return usagef("say how far: --dy or --dx")
	}
	x, y, err := placeOf(words[1], words[2])
	if err != nil {
		return err
	}
	act := api.ScreenAction{Type: "scroll", X: x, Y: y}
	if *dy != 0 {
		act.DY = dy
	}
	if *dx != 0 {
		act.DX = dx
	}
	return a.workScreen(ctx, words[0], *scale, act)
}

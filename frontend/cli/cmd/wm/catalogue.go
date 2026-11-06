package main

import (
	"context"
	"fmt"
	"strconv"

	"wheelmachine/frontend/cli/api"
)

func (a *app) gpus(ctx context.Context, args []string) error {
	fs := a.flags("gpus", "wm gpus", "GPUs on offer now, where they are and what an hour costs, in the account's currency. Give an id to wm machines "+
		"create --gpu.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 0, 0, ""); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	list, err := c.GPUs(ctx)
	if err != nil {
		return err
	}
	switch {
	case a.json:
		if list.Data == nil {
			list.Data = []api.GpuOffer{}
		}
		return a.printJSON(list)
	case a.quiet:
		for _, g := range list.Data {
			fmt.Fprintln(a.stdout, g.ID)
		}
		return nil
	}
	t := a.table("ID", "GPU", "MEMORY", "PRICE", "LOCATION", "AVAILABLE", "INTERRUPTIBLE")
	for _, g := range list.Data {
		gpu := g.Model
		if g.Count > 1 {
			gpu = strconv.Itoa(g.Count) + " x " + gpu
		}
		t.row(g.ID, gpu, strconv.Itoa(g.MemoryGB)+" GB", moneyIn(g.PricePerHour, or(g.Currency, list.Currency))+"/h", place(g.Location), yesNo(g.Available),
			yesNo(g.Interruptible))
	}
	if err := t.flush(); err != nil {
		return err
	}
	if !list.ForSale {
		fmt.Fprintln(a.stderr, "wm: GPU machines are not open to every account yet.")
	}
	return nil
}

func (a *app) regions(ctx context.Context, args []string) error {
	fs := a.flags("regions", "wm regions", "Places you can ask a machine to run in (wm machines create --region). A "+
		"machine says where it actually runs in its location.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 0, 0, ""); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	list, err := c.Regions(ctx)
	if err != nil {
		return err
	}
	switch {
	case a.json:
		if list == nil {
			list = []api.Region{}
		}
		return a.printJSON(list)
	case a.quiet:
		for _, r := range list {
			fmt.Fprintln(a.stdout, r.ID)
		}
		return nil
	}
	t := a.table("ID", "CITY", "COUNTRY")
	for _, r := range list {
		t.row(r.ID, r.City, r.Country)
	}
	return t.flush()
}

package protocol

import (
	"testing"
	"time"
)

func at(h, m int) time.Time { return time.Date(2026, 6, 23, h, m, 0, 0, time.UTC) }

func TestActivePrice(t *testing.T) {
	o := ModelOffer{PriceIn: 0.20, PriceOut: 0.30, Schedule: []PriceWindow{
		{Start: "03:00", End: "03:30", Free: true},          // free 30 min/day
		{Start: "18:00", End: "22:00", In: 0.50, Out: 0.70}, // peak
		{Start: "22:00", End: "02:00", In: 0.05, Out: 0.05}, // overnight (wraps midnight)
	}}
	cases := []struct {
		h, m      int
		in, out   float64
		free      bool
		scheduled bool
	}{
		{3, 15, 0, 0, true, true},         // free window
		{12, 0, 0.20, 0.30, false, false}, // base/fallback (not scheduled)
		{20, 0, 0.50, 0.70, false, true},  // peak
		{23, 30, 0.05, 0.05, false, true}, // overnight after 22:00
		{1, 0, 0.05, 0.05, false, true},   // overnight before 02:00 (wrap)
	}
	for _, c := range cases {
		in, out, free, scheduled := o.ActivePrice(at(c.h, c.m))
		if in != c.in || out != c.out || free != c.free || scheduled != c.scheduled {
			t.Errorf("%02d:%02d -> %v/%v free=%v sched=%v, want %v/%v free=%v sched=%v", c.h, c.m, in, out, free, scheduled, c.in, c.out, c.free, c.scheduled)
		}
	}
}

func TestActivePriceNoSchedule(t *testing.T) {
	o := ModelOffer{PriceIn: 0.1, PriceOut: 0.2}
	if in, out, free, scheduled := o.ActivePrice(at(12, 0)); in != 0.1 || out != 0.2 || free || scheduled {
		t.Errorf("no schedule = %v/%v free=%v sched=%v, want base/false/false", in, out, free, scheduled)
	}
}

func TestDayRestrictedWindow(t *testing.T) {
	day := int(at(12, 0).Weekday())
	other := (day + 3) % 7
	o := ModelOffer{PriceIn: 0.2, Schedule: []PriceWindow{{Days: []int{other}, Start: "00:00", End: "23:59", Free: true}}}
	if _, _, free, _ := o.ActivePrice(at(12, 0)); free {
		t.Error("free window applied on a day not in Days")
	}
	o.Schedule[0].Days = []int{day}
	if _, _, free, _ := o.ActivePrice(at(12, 0)); !free {
		t.Error("free window should apply on the listed day")
	}
}

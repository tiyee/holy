package helps

import (
	"testing"
	"time"
)

func TestCalendarBoundaries(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range []*time.Location{ny, time.UTC, time.FixedZone("UTC+14", 14*3600), time.FixedZone("UTC-12", -12*3600)} {
		for _, date := range []time.Time{time.Date(2026, 3, 8, 12, 34, 56, 0, loc), time.Date(2026, 11, 1, 12, 34, 56, 0, loc), time.Date(1969, 12, 31, 23, 0, 0, 0, loc), time.Date(2024, 2, 29, 12, 0, 0, 0, loc)} {
			want := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, loc)
			if got := time.Unix(FirstSecond(date), 0); !got.Equal(want) {
				t.Fatalf("day boundary: %s want %s", got, want)
			}
			last := time.Date(date.Year(), date.Month()+1, 0, 0, 0, 0, 0, loc)
			if got := GetLastDateOfMonth(date); !got.Equal(last) || got.Location() != loc {
				t.Fatalf("month boundary: %s want %s", got, last)
			}
			start, end := MonthRange(date)
			if !start.Equal(GetFirstDateOfMonth(date)) || !end.Equal(last.AddDate(0, 0, 1)) {
				t.Fatal("invalid month range")
			}
		}
	}
}
func TestFirstSecond2UsesDateOffset(t *testing.T) {
	before := time.Local
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	time.Local = ny
	t.Cleanup(func() { time.Local = before })
	for _, month := range []time.Month{time.January, time.July} {
		date := time.Date(2026, month, 10, 12, 0, 0, 0, ny)
		if FirstSecond2(date.Unix()) != GetZeroTime(date).Unix() {
			t.Fatal("stale timezone offset")
		}
	}
	end := time.Date(2026, 1, 1, 13, 42, 17, 0, ny)
	if got := Duration2(end.Unix(), end.Unix()); got != "2026-01-01 13:42:17 - 13:42" {
		t.Fatal(got)
	}
}

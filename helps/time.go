package helps

import (
	"strings"
	"time"
)

const OneDaySec = int64(86400)

func FormatTime(ts int64) string {
	t := time.Unix(ts, 0)
	return t.Format(time.DateTime)
}

func FormatDate(ts int64) string {
	t := time.Unix(ts, 0)
	return t.Format(time.DateOnly)
}

func StrToTime(str string) int64 {
	loc, _ := time.LoadLocation("Local")
	theTime, err := time.ParseInLocation(time.DateTime, str, loc)
	if err != nil {
		return 0
	}
	return theTime.Unix()
}

func Str2Time(str string) time.Time {
	loc, _ := time.LoadLocation("Local")
	theTime, _ := time.ParseInLocation(time.DateTime, str, loc)
	return theTime
}
func Duration(s, e int64) string {
	var start string
	var end string
	if s == 0 {
		start = ""
	} else {
		start = time.Unix(int64(s), 0).Format(time.DateTime)
	}
	if e == 0 {
		end = ""
	} else {
		end = time.Unix(int64(e), 0).Format(time.DateTime)
	}
	return start + " - " + end
}
func Duration2(start, end int64) string {
	return strings.Join([]string{
		time.Unix(start, 0).Format(time.DateTime),
		" - ",
		time.Unix(end, 0).Format("15:04"),
	}, "")
}
func TodayFirstSecond() int64 {
	return FirstSecond(time.Now())
}
func FirstSecond(t time.Time) int64 { return GetZeroTime(t).Unix() }
func FirstSecond2(s int64) int64    { return FirstSecond(time.Unix(s, 0)) }
func DurationSecondsOfMonth(s int64) (int64, int64) {
	t := time.Unix(s, 0)
	return GetFirstDateOfMonth(t).Unix(), GetLastDateOfMonth(t).Unix()
}

// GetFirstDateOfMonth 返回输入时区中该月第一天的零点。
func GetFirstDateOfMonth(d time.Time) time.Time {
	return time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, d.Location())
}

// GetLastDateOfMonth 返回输入时区中该月最后一天的零点。
func GetLastDateOfMonth(d time.Time) time.Time {
	return time.Date(d.Year(), d.Month()+1, 0, 0, 0, 0, 0, d.Location())
}

// GetZeroTime 返回输入日期所在时区的零点。
func GetZeroTime(d time.Time) time.Time {
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, d.Location())
}

// MonthRange 返回月份的半开区间 [月初, 下月月初)，适用于数据库范围查询。
func MonthRange(d time.Time) (time.Time, time.Time) {
	start := GetFirstDateOfMonth(d)
	return start, start.AddDate(0, 1, 0)
}

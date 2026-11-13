package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agentenv"
)

// A balance field with several amounts, or a percent, comes to the card
// each amount apart, its label apart from its figure and a percent's
// share for a meter (#420); one amount alone is the figure as it was.
func TestBalanceParts(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}
	keyBalanceCache.data = nil
	t.Cleanup(func() { keyBalanceCache.data = nil })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"total_available":1000000,"today":2.5,"used":45,"granted":50}}`))
	}))
	defer srv.Close()
	for _, p := range []Provider{
		{ID: "several", Name: "Several", Key: "sk-1", BalanceURL: srv.URL + "/b", BalancePath: "Balance: $data.total_available / 500000; Today: $data.today; Used: data.used / data.granted %"},
		{ID: "share", Name: "Share", Key: "sk-2", BalanceURL: srv.URL + "/b", BalancePath: "data.used / data.granted %"},
		{ID: "one", Name: "One", Key: "sk-3", BalanceURL: srv.URL + "/b", BalancePath: "$data.total_available / 500000"},
	} {
		p.Chat = srv.URL + "/v1"
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	got := map[string]SubscriptionQuota{}
	for _, q := range KeyBalances(context.Background()) {
		got[q.Provider] = q
	}
	parts := func(id string) string {
		b, _ := json.Marshal(got[id].BalanceParts)
		return string(b)
	}
	if q := got["several"]; q.Balance != "Balance $2.00 · Today $2.50 · Used 90%" {
		t.Errorf("several: the line as it was, got %q", q.Balance)
	}
	if want := `[{"label":"Balance","text":"$2.00"},{"label":"Today","text":"$2.50"},{"label":"Used","text":"90%","percent":90}]`; parts("several") != want {
		t.Errorf("several: %s\nwant %s", parts("several"), want)
	}
	if want := `[{"text":"90%","percent":90}]`; parts("share") != want {
		t.Errorf("share: %s\nwant %s", parts("share"), want)
	}
	if q := got["one"]; q.Balance != "$2.00" || q.BalanceParts != nil {
		t.Errorf("one: %q %s, want the figure alone", q.Balance, parts("one"))
	}
	// when each was read, for the card to say, which the minute the
	// balances are kept for leaves as it was
	for id, q := range got {
		if q.ReadAt == nil || time.Since(*q.ReadAt) > time.Minute {
			t.Errorf("%s: read at %v", id, q.ReadAt)
		}
	}
	first := *got["one"].ReadAt
	for _, q := range KeyBalances(context.Background()) {
		if q.Provider == "one" && !q.ReadAt.Equal(first) {
			t.Errorf("one: kept, it says read at %v, not %v", q.ReadAt, first)
		}
	}
}

// A part of a balance field ending in "@" is a time (readBalanceTime): a
// Unix time in seconds or in milliseconds, RFC 3339 text or a date alone,
// shown as its date — today's as its time of day — instead of as an amount
// of 1796832000000.00. skygunner's #3011 and hong65201's #5660 asked for
// it: a panel's resets_at came to the card as a number.
func TestBalanceFieldTime(t *testing.T) {
	body := []byte(`{"quota":{"resetsAt":1796832000,"resetsMs":1796832000000,"text":"2026-12-10T00:00:00+08:00","day":"2026-12-10"},"n":5}`)
	local := time.Date(2026, 12, 10, 0, 0, 0, 0, time.Local)
	if local.Month() != 12 || local.Day() != 10 {
		t.Fatalf("the fixture's date: %v", local)
	}
	for _, c := range []struct{ path, want string }{
		{"quota.resetsAt@", "Dec 10"},
		{"quota.resetsMs@", "Dec 10"},
		{"quota.text@", "Dec 10"},
		{"quota.day@", "Dec 10"},
		{"重置: quota.resetsAt@; n: n", "重置 Dec 10 · n 5.00"},
	} {
		if got, err := readBalancePath(body, c.path); err != nil || got != c.want {
			t.Errorf("%q: %q %v, want %q", c.path, got, err, c.want)
		}
	}
	// today's is its time of day: what a card has room for
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 9, 30, 0, 0, time.Local)
	if got, err := readBalancePath([]byte(`{"t":`+strconv.FormatInt(today.Unix(), 10)+`}`), "t@"); err != nil || got != "09:30" {
		t.Errorf("today: %q %v, want 09:30", got, err)
	}
	// a time the reply hasn't is "—" as an amount is, and the others show
	parts, err := readBalanceParts(body, "at: quota.nope@; n: n")
	if err != nil || len(parts) != 2 || parts[0].Text != "—" || parts[1].Text != "5.00" {
		t.Errorf("parts: %+v %v", parts, err)
	}
	// a number that isn't a time, or text that isn't a date, says so
	for _, path := range []string{"n@", "quota.day@x", "@"} {
		if got, err := readBalancePath(body, path); err == nil {
			t.Errorf("%q: read %q, want an error", path, got)
		}
	}
	if _, err := readBalancePath([]byte(`{"d":"not a date"}`), "d@"); err == nil || !strings.Contains(err.Error(), "not a date") {
		t.Errorf("text: %v", err)
	}
}

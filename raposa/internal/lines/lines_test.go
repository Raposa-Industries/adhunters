package lines

import "testing"

func TestParse(t *testing.T) {
	got := Parse(`
# the capture boxes' file
dc-us-1=10.0.0.1:8000:u1:p1
isp-2=10.0.0.2:8000:u2:p2
isp-7=10.0.0.7:8000:u7:p7
res-1="geo.example.com:12321:user:pw_country-us_session-AB:CD"
broken line
bad-port=10.0.0.9:x:u:p
`)
	want := []Line{
		{Key: "dc-us-1", Role: "dc", Host: "10.0.0.1", Port: 8000, Username: "u1", Password: "p1"},
		{Key: "isp-7", Role: "isp", Host: "10.0.0.7", Port: 8000, Username: "u7", Password: "p7"},
		{Key: "res-1", Role: "residential", Host: "geo.example.com", Port: 12321, Username: "user", Password: "pw_country-us_session-AB:CD"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if u := got[0].URL().String(); u != "http://u1:p1@10.0.0.1:8000" {
		t.Errorf("url = %s", u)
	}
}

func TestFailedLinesRest(t *testing.T) {
	s := New(Parse("dc-us-1=h:1:u:p\ndc-us-2=h:2:u:p"))
	s.Failed("dc-us-1")
	s.Failed("dc-us-1")
	if len(s.All()) != 2 {
		t.Fatal("a line rested before its third failure")
	}
	s.Failed("dc-us-1")
	if all := s.All(); len(all) != 1 || all[0].Key != "dc-us-2" {
		t.Fatalf("after three failures: %+v", all)
	}
}

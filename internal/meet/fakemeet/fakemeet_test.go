package fakemeet

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestServesPageAndScenario(t *testing.T) {
	s := NewWithScenario(Scenario{Lobby: true})
	defer s.Close()
	res, err := http.Get(s.URL + "/abc-defg-hij?hl=en")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), "Join now") {
		t.Fatal("page.html not served")
	}
	res, err = http.Get(s.URL + "/api/scenario")
	if err != nil {
		t.Fatal(err)
	}
	var sc Scenario
	if err := json.NewDecoder(res.Body).Decode(&sc); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if !sc.Lobby || sc.SelfName != "Slonik Bot (transcribing)" {
		t.Fatalf("%+v", sc)
	}
}

type command struct {
	Seq     int    `json:"seq"`
	Op      string `json:"op"`
	Speaker string `json:"speaker"`
}

func commands(t *testing.T, s *Server, after int) []command {
	t.Helper()
	res, err := http.Get(s.URL + "/api/commands?after=" + strconv.Itoa(after))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var cmds []command
	if err := json.NewDecoder(res.Body).Decode(&cmds); err != nil {
		t.Fatal(err)
	}
	return cmds
}

func TestCommandsAndReports(t *testing.T) {
	s := New()
	defer s.Close()
	s.Caption(1, "Alice Example", "Hello")
	s.End()
	cmds := commands(t, s, 0)
	if len(cmds) != 2 || cmds[0].Speaker != "Alice Example" || cmds[1].Seq <= cmds[0].Seq {
		t.Fatalf("%+v", cmds)
	}
	if more := commands(t, s, cmds[1].Seq); len(more) != 0 {
		t.Fatalf("expected no new commands, got %+v", more)
	}
	for _, r := range []string{`{"kind":"joined"}`, `{"kind":"chat","value":"hi"}`, `{"kind":"language","value":"French"}`, `{"kind":"left"}`} {
		res, err := http.Post(s.URL+"/api/report", "application/json", strings.NewReader(r))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
	}
	if !s.Joined() || !s.Left() || s.Language() != "French" {
		t.Fatal("reports not recorded")
	}
	if got := s.Chat(); len(got) != 1 || got[0] != "hi" {
		t.Fatalf("chat %q", got)
	}
}

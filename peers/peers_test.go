package peers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewStore(t *testing.T) {
	s := NewStore("/tmp/test-peers.json")
	if s == nil {
		t.Fatal("nil store")
	}
	if s.Len() != 0 {
		t.Fatalf("expected 0 peers, got %d", s.Len())
	}
}

func TestAddAndGet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peers.json")
	s := NewStore(path)

	p, isNew := s.Add("https://node1.example.com")
	if p == nil {
		t.Fatal("Add returned nil")
	}
	if !isNew {
		t.Fatal("expected isNew=true")
	}
	if p.URL != "https://node1.example.com" {
		t.Fatalf("wrong URL: %s", p.URL)
	}
	if p.Score != DefaultMinScore {
		t.Fatalf("expected score %d, got %d", DefaultMinScore, p.Score)
	}

	// Get
	p2 := s.Get("https://node1.example.com")
	if p2 == nil {
		t.Fatal("Get returned nil")
	}
	if p2.URL != p.URL {
		t.Fatalf("URL mismatch")
	}

	// Add duplicate
	p3, isNew2 := s.Add("https://node1.example.com")
	if s.Len() != 1 {
		t.Fatalf("duplicate added, len=%d", s.Len())
	}
	if isNew2 {
		t.Fatal("duplicate should report isNew=false")
	}
	if p3 != p {
		t.Fatal("duplicate returned different pointer")
	}
}

func TestAddWithTrailingSlash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peers.json")
	s := NewStore(path)

	s.Add("https://node1.example.com/")
	s.Add("https://node1.example.com")
	if s.Len() != 1 {
		t.Fatalf("trailing slash duplicate not deduped: %d", s.Len())
	}
}

func TestRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peers.json")
	s := NewStore(path)

	s.Add("https://node1.example.com")
	s.Add("https://node2.example.com")

	if !s.Remove("https://node1.example.com") {
		t.Fatal("Remove returned false")
	}
	if s.Len() != 1 {
		t.Fatalf("expected 1, got %d", s.Len())
	}
	if s.Get("https://node1.example.com") != nil {
		t.Fatal("peer still exists after remove")
	}
	if s.Remove("https://nonexistent.example.com") {
		t.Fatal("removing nonexistent should return false")
	}
}

func TestUpdateScore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peers.json")
	s := NewStore(path)

	s.Add("https://good.example.com")
	s.Add("https://bad.example.com")

	s.RecordSuccess("https://good.example.com")
	s.RecordSuccess("https://good.example.com")
	s.RecordSuccess("https://good.example.com")

	s.RecordMismatch("https://bad.example.com")
	s.RecordMismatch("https://bad.example.com")
	s.RecordTimeout("https://bad.example.com")

	good := s.Get("https://good.example.com")
	if good.Score != 3 { // 0 + 1 + 1 + 1
		t.Fatalf("good peer score: expected 3, got %d", good.Score)
	}
	if good.SuccessCount != 3 {
		t.Fatalf("good peer success: expected 3, got %d", good.SuccessCount)
	}

	bad := s.Get("https://bad.example.com")
	expected := 0 + ScoreMismatch + ScoreMismatch + ScoreTimeout
	if bad.Score != expected {
		t.Fatalf("bad peer score: expected %d, got %d", expected, bad.Score)
	}
}

func TestNoKickOnLowScore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peers.json")
	s := NewStore(path)

	s.Add("https://doomed.example.com")
	// Score goes very negative but peer is NOT kicked
	for i := 0; i < 10; i++ {
		s.RecordMismatch("https://doomed.example.com") // -2 each → -20
	}
	if s.Get("https://doomed.example.com") == nil {
		t.Fatal("peer should NOT be kicked — only score is lowered")
	}
	if s.Len() != 1 {
		t.Fatalf("expected 1 peer, got %d", s.Len())
	}
	// Score should be very negative
	p := s.Get("https://doomed.example.com")
	if p.Score > -15 {
		t.Fatalf("expected score <= -15, got %d", p.Score)
	}
	t.Logf("Peer retained with score=%d (not kicked)", p.Score)
}

func TestTop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peers.json")
	s := NewStore(path)

	s.Add("https://low.example.com")
	s.Add("https://mid.example.com")
	s.Add("https://high.example.com")

	s.RecordSuccess("https://high.example.com") // score 1
	s.RecordSuccess("https://high.example.com") // score 2
	s.RecordSuccess("https://mid.example.com")  // score 1

	top := s.Top(2)
	if len(top) != 2 {
		t.Fatalf("expected 2, got %d", len(top))
	}
	if top[0].URL != "https://high.example.com" {
		t.Fatalf("top[0] should be high, got %s", top[0].URL)
	}
	if top[1].URL != "https://mid.example.com" {
		t.Fatalf("top[1] should be mid, got %s", top[1].URL)
	}
}

func TestMaxPeersLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peers.json")
	s := NewStore(path)

	// Add MaxPeers + 10
	for i := 0; i < MaxPeers+10; i++ {
		url := "https://node-" + string(rune('a'+i%26)) + ".example.com/" + string(rune('0'+i/26))
		s.Add(url)
	}
	if s.Len() > MaxPeers {
		t.Fatalf("expected at most %d peers, got %d", MaxPeers, s.Len())
	}
}

func TestSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peers.json")
	s := NewStore(path)

	s.Add("https://peer1.example.com")
	s.Add("https://peer2.example.com")
	s.RecordSuccess("https://peer1.example.com")

	if err := s.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("peers.json was not created")
	}

	// Load into a new store
	s2 := NewStore(path)
	if err := s2.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if s2.Len() != 2 {
		t.Fatalf("expected 2, got %d", s2.Len())
	}
	if s2.Get("https://peer1.example.com").Score != 1 {
		t.Fatalf("score not preserved: %d", s2.Get("https://peer1.example.com").Score)
	}
}

func TestLoadNonexistent(t *testing.T) {
	s := NewStore("/tmp/nonexistent/peers-test.json")
	if err := s.Load(); err != nil {
		t.Fatalf("loading nonexistent file should not error: %v", err)
	}
}

func TestAddPeers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peers.json")
	s := NewStore(path)

	added := s.AddPeers([]string{
		"https://a.example.com",
		"https://b.example.com",
		"https://a.example.com", // duplicate
		"",
	})
	if added != 2 {
		t.Fatalf("expected 2 added, got %d", added)
	}
	if s.Len() != 2 {
		t.Fatalf("expected 2, got %d", s.Len())
	}
}

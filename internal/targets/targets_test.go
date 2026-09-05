package targets

import (
	"testing"
	"testing/fstest"
)

const header = "url,category_code,category_description,date_added,source,notes\n"

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"controls.csv": &fstest.MapFile{Data: []byte(header +
			"https://ya.ru/,CTRL-RU,Control - domestic,2026-09-04,rnfo,ru\n" +
			"https://example.com/,CTRL-INTL,Control - international,2026-09-04,rnfo,intl\n")},
		"citizenlab-ru.csv": &fstest.MapFile{Data: []byte(header +
			"https://a.example/,NEWS,News,2020-01-01,citizenlab,\n" +
			"https://b.example/,HUMR,Human rights,2020-01-01,citizenlab,\n" +
			"not a url,NEWS,broken,2020-01-01,citizenlab,\n" +
			"https://a.example/,NEWS,duplicate,2020-01-01,citizenlab,\n")},
		"citizenlab-global.csv": &fstest.MapFile{Data: []byte(header +
			"https://c.example/,POLR,Politics,2020-01-01,citizenlab,\n" +
			"https://b.example/,HUMR,also in ru list,2020-01-01,citizenlab,\n")},
	}
}

func TestLoadDedupesAndKeepsProvenance(t *testing.T) {
	own := []Target{{URL: "https://203.0.113.5/v1/health", Host: "203.0.113.5", Category: "OWN-RESPONDER"}}
	set, err := Load(testFS(), "full", own)
	if err != nil {
		t.Fatal(err)
	}
	// 2 controls + 1 own + a, b from ru + c from global; b in global and the
	// duplicate a in ru are dropped; the malformed row is skipped.
	if got := len(set.Targets); got != 6 {
		t.Fatalf("got %d targets, want 6: %+v", got, set.Targets)
	}
	byURL := map[string]Target{}
	for _, x := range set.Targets {
		byURL[x.URL] = x
	}
	if byURL["https://b.example/"].List != "citizenlab-ru" {
		t.Errorf("first list wins for a duplicate URL; got %q", byURL["https://b.example/"].List)
	}
	if byURL["https://ya.ru/"].Scope != "ru" || byURL["https://example.com/"].Scope != "intl" {
		t.Error("control scope not parsed from the notes column")
	}
	if !byURL["https://ya.ru/"].IsControl() || byURL["https://ya.ru/"].IsIntlControl() {
		t.Error("ya.ru is a domestic control, not an international one")
	}
	if !byURL["https://example.com/"].IsIntlControl() {
		t.Error("example.com is an international control")
	}
	if byURL["https://203.0.113.5/v1/health"].List != "own" {
		t.Error("own targets must be tagged list=own")
	}
	if total, intl := set.Controls(); total != 2 || intl != 1 {
		t.Errorf("Controls() = %d/%d, want 2/1", total, intl)
	}
	for _, name := range []string{"controls", "citizenlab-ru", "citizenlab-global"} {
		if len(set.Manifest[name]) != 64 {
			t.Errorf("manifest for %s missing or not a sha256", name)
		}
	}
}

func TestControlsProfileIsSmall(t *testing.T) {
	set, err := Load(testFS(), "controls", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Targets) != 2 {
		t.Errorf("controls profile should hold only controls (and own), got %d", len(set.Targets))
	}
	if _, err := Load(testFS(), "nonsense", nil); err == nil {
		t.Error("unknown profile must be an error")
	}
}

// Order must be a pure function of the run id: two probes in the same slot
// produce the same order, and controls come first regardless of the shuffle.
func TestOrderIsDeterministicPerRunAndControlsFirst(t *testing.T) {
	a, _ := Load(testFS(), "full", nil)
	b, _ := Load(testFS(), "full", nil)
	a.Order("2026-09-05T00:00Z/full")
	b.Order("2026-09-05T00:00Z/full")
	for i := range a.Targets {
		if a.Targets[i].URL != b.Targets[i].URL {
			t.Fatalf("same run id, different order at %d: %s vs %s", i, a.Targets[i].URL, b.Targets[i].URL)
		}
	}
	total, _ := a.Controls()
	for i := 0; i < total; i++ {
		if !a.Targets[i].IsControl() {
			t.Fatalf("position %d should be a control, got %s", i, a.Targets[i].URL)
		}
	}
	c, _ := Load(testFS(), "full", nil)
	c.Order("2026-09-05T06:00Z/full")
	same := true
	for i := range a.Targets {
		if a.Targets[i].URL != c.Targets[i].URL {
			same = false
			break
		}
	}
	// With three non-control targets there are six orders; a different seed
	// giving the same one is possible but unlikely. Not a hard failure.
	if same {
		t.Log("different run ids produced the same order; possible with this few targets")
	}
}

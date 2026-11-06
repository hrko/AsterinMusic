package discordbot

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"asterinmusic/internal/music"
)

func editorFixture(n int) *queueEditor {
	v := &queueEditor{id: "test", owner: 2, guild: 1, voice: 4, message: 5, expires: time.Now().Add(queueEditorTTL), requester: "2", page: 1, ownerPage: 1, permissions: map[string]queueEditPermission{}}
	for i := 1; i <= n; i++ {
		id := fmt.Sprint(i)
		v.entries = append(v.entries, music.Entry{ID: id, Requester: music.Requester{ID: "2"}, Track: music.Track{Title: "長いタイトル [Live] " + strings.Repeat("🎵音楽", 30), URL: "https://www.youtube.com/watch?v=TcLLpZBWsck", Site: "youtube"}})
		v.permissions[id] = queueEditPermission{remove: true, reorder: true}
	}
	return v
}

func TestQueueEditorLayoutBoundaries(t *testing.T) {
	for _, tc := range []struct {
		page       int
		id, indent string
	}{{1, "10", "   "}, {2, "12", "    "}, {10, "100", "    "}} {
		v := editorFixture(105)
		v.page, v.selected = tc.page, tc.id
		e, components := v.render()
		if !strings.Contains(e.Description, tc.id+". ▶ **") {
			t.Fatal("selection marker outside list")
		}
		if strings.Count(e.Description, "\n"+tc.indent+"-# ") != 10 {
			t.Fatal("continuation indentation changed at digit boundary")
		}
		if strings.Count(e.Description, "▶") != 1 || e.Thumbnail != nil || len(e.Description) > 4096 {
			t.Fatal("layout bounds")
		}
		if len(components) != 4 {
			t.Fatal("unexpected action rows")
		}
		b, err := json.Marshal(components)
		if err != nil || !strings.Contains(string(b), "qedit:test:0:remove") {
			t.Fatal("invalid components", err)
		}
	}
}

func TestQueueEditorSelectionAndNavigation(t *testing.T) {
	v := editorFixture(23)
	if v.selectEntry("11") || !v.selectEntry("10") {
		t.Fatal("off-page selection")
	}
	v.entries[9], v.entries[10] = v.entries[10], v.entries[9]
	v.reconcile(true)
	if v.page != 2 || v.selected != "10" {
		t.Fatal("move did not follow selected item")
	}
	v.page = 1
	v.reconcile(false)
	if v.selected != "" {
		t.Fatal("off-page selection retained")
	}
	v.permissions["1"] = queueEditPermission{}
	if v.selectEntry("1") {
		t.Fatal("unauthorized item selected")
	}
	v.selected = "missing"
	v.reconcile(true)
	if v.selected != "" {
		t.Fatal("removed item retained")
	}
	v.entries = nil
	v.page = 10
	v.reconcile(false)
	if v.page != 1 {
		t.Fatal("empty page not clamped")
	}
}

func TestQueueEditorOwnersAndLifetime(t *testing.T) {
	v := editorFixture(0)
	for i := 1; i <= 26; i++ {
		v.owners = append(v.owners, queueEditOwner{requester: music.Requester{ID: fmt.Sprint(i), Name: "同名"}, count: i})
	}
	v.choosingOwner = true
	v.ownerPage = 3
	e, c := v.render()
	if strings.Count(e.Description, "- **<@") != 6 || !strings.Contains(e.Description, "<@26>") {
		t.Fatal("owners truncated")
	}
	b, _ := json.Marshal(c)
	if strings.Contains(string(b), "ID:") || !strings.Contains(string(b), "待機26曲") {
		t.Fatal("owner descriptions must show counts without numeric IDs")
	}
	if !v.valid(2, 1, 4, 5, 0, time.Now()) || v.valid(3, 1, 4, 5, 0, time.Now()) || v.valid(2, 1, 9, 5, 0, time.Now()) || v.valid(2, 1, 4, 6, 0, time.Now()) || v.valid(2, 1, 4, 5, 1, time.Now()) || v.valid(2, 1, 4, 5, 0, v.expires) {
		t.Fatal("view boundaries")
	}
	a := &App{}
	for i := 0; i < queueEditorLimit; i++ {
		if !a.rememberQueueEditor(&queueEditor{id: fmt.Sprint(i), expires: v.expires}) {
			t.Fatal("limit too small")
		}
	}
	if a.rememberQueueEditor(v) {
		t.Fatal("live views evicted")
	}
	a.queueEditors["0"].expires = time.Now().Add(-time.Second)
	if !a.rememberQueueEditor(v) {
		t.Fatal("expired view not reclaimed")
	}
}

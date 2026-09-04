package httpapi

import "testing"

type paginationFixture struct {
	ID string
}

func TestPageReturnsNilCursorWhenFetchFitsPage(t *testing.T) {
	items := []paginationFixture{{ID: "a"}, {ID: "b"}}
	got, cursor := page(items, 2, func(item paginationFixture) string { return item.ID })
	if len(got) != 2 || cursor != nil {
		t.Fatalf("page = %#v, cursor = %v", got, cursor)
	}
}

func TestPageTrimsExtraItemAndReturnsLastVisibleID(t *testing.T) {
	items := []paginationFixture{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	got, cursor := page(items, 2, func(item paginationFixture) string { return item.ID })
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("page = %#v", got)
	}
	if cursor == nil || *cursor != "b" {
		t.Fatalf("cursor = %v, want b", cursor)
	}
	if len(items) != 3 {
		t.Fatalf("page mutated source length to %d", len(items))
	}
}

func TestPageHandlesEmptyPage(t *testing.T) {
	got, cursor := page([]paginationFixture{}, 50, func(item paginationFixture) string { return item.ID })
	if got == nil || len(got) != 0 || cursor != nil {
		t.Fatalf("empty page = %#v, cursor = %v", got, cursor)
	}
}

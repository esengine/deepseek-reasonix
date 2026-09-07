package control

import (
	"testing"

	"reasonix/internal/skill"
)

func TestSlashArgItemsLazyResolvesOnlyDynamicStructuredCommands(t *testing.T) {
	data := ArgData{Skills: []skill.Skill{{Name: "warm"}}}
	calls := 0
	resolve := func() ArgData {
		calls++
		return data
	}

	if items, _, applies := SlashArgItemsLazy("/custom free text", resolve); applies || len(items) != 0 {
		t.Fatalf("free-form command unexpectedly applied: %+v", items)
	}
	if calls != 0 {
		t.Fatalf("free-form command resolved dynamic data %d times", calls)
	}

	items, _, applies := SlashArgItemsLazy("/language e", resolve)
	if !applies || len(items) != 2 || items[0].Label != "en" || items[1].Label != "es" {
		t.Fatalf("static structured completion = %+v, applies=%v", items, applies)
	}
	if calls != 0 {
		t.Fatalf("static structured command resolved dynamic data %d times", calls)
	}

	items, _, applies = SlashArgItemsLazy("/skills show w", resolve)
	if !applies || len(items) != 1 || items[0].Label != "warm" {
		t.Fatalf("dynamic structured completion = %+v, applies=%v", items, applies)
	}
	if calls != 1 {
		t.Fatalf("dynamic structured command resolved data %d times, want 1", calls)
	}
}

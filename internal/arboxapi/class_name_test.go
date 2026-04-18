package arboxapi

import "testing"

func TestClass_ResolvedCategoryName_primary(t *testing.T) {
	c := Class{}
	c.BoxCategories.Name = "  Hall B  "
	if got := c.ResolvedCategoryName(); got != "Hall B" {
		t.Fatalf("got %q", got)
	}
}

func TestClass_ResolvedCategoryName_fromRaw(t *testing.T) {
	var c Class
	c.BoxCategories.Name = ""
	c.Raw = map[string]any{"class_name": "  WOD Hall A  "}
	if got := c.ResolvedCategoryName(); got != "WOD Hall A" {
		t.Fatalf("got %q", got)
	}
}

func TestClass_YouStatus_fromRawUserBooked(t *testing.T) {
	var c Class
	c.Raw = map[string]any{"user_booked": float64(991)}
	if got := c.YouStatus(); got != "BOOKED" {
		t.Fatalf("got %q", got)
	}
}

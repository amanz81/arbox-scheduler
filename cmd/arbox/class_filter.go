package main

import (
	"strings"

	"github.com/lafofo-nivo/arbox-scheduler/internal/config"
)

// classPassesGlobalFilter applies the config-level category_filter only
// (include + exclude substring rules). Used when listing real Arbox classes
// for /setup before per-option category matching.
func classPassesGlobalFilter(name string, flt config.CategoryFilter) bool {
	nameLower := strings.ToLower(name)
	for _, ex := range flt.Exclude {
		if ex != "" && strings.Contains(nameLower, strings.ToLower(ex)) {
			return false
		}
	}
	if len(flt.Include) == 0 {
		return true
	}
	for _, inc := range flt.Include {
		if inc != "" && strings.Contains(nameLower, strings.ToLower(inc)) {
			return true
		}
	}
	return false
}

package pipeline

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
)

const (
	maxMatrixAxes   = 8
	maxCombinations = 16
)

// varRe matches ${NAME} references to matrix axes.
var varRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Matrix maps axis names to their values; each combination of values runs
// as its own job.
type Matrix map[string][]string

// Combinations returns every combination of the matrix: axes in name
// order, values in the order listed. An empty matrix has none.
func (m Matrix) Combinations() []map[string]string {
	if len(m) == 0 {
		return nil
	}
	combos := []map[string]string{{}}
	for _, axis := range slices.Sorted(maps.Keys(m)) {
		var next []map[string]string
		for _, c := range combos {
			for _, v := range m[axis] {
				nc := maps.Clone(c)
				nc[axis] = v
				next = append(next, nc)
			}
		}
		combos = next
	}
	return combos
}

// validate reports the problems of a matrix.
func (m Matrix) validate() []error {
	var problems []error
	if len(m) > maxMatrixAxes {
		problems = append(problems, fmt.Errorf("matrix: at most %d axes are allowed, got %d", maxMatrixAxes, len(m)))
	}
	n := 1
	for _, axis := range slices.Sorted(maps.Keys(m)) {
		values := m[axis]
		switch {
		case !envNameRe.MatchString(axis):
			problems = append(problems, fmt.Errorf("matrix: axis name %q is invalid", axis))
		case reservedEnv(axis):
			problems = append(problems, fmt.Errorf("matrix: axis %q is reserved", axis))
		}
		if len(values) == 0 {
			problems = append(problems, fmt.Errorf("matrix: axis %q has no values", axis))
		}
		if len(slices.Compact(slices.Sorted(slices.Values(values)))) != len(values) {
			problems = append(problems, fmt.Errorf("matrix: axis %q lists a value twice", axis))
		}
		n *= max(len(values), 1)
	}
	if n > maxCombinations {
		problems = append(problems, fmt.Errorf("matrix: %d combinations, at most %d are allowed", n, maxCombinations))
	}
	return problems
}

// undefinedVars returns the ${NAME} references in s that are not axes.
func (m Matrix) undefinedVars(s string) []string {
	var missing []string
	for _, ref := range varRe.FindAllStringSubmatch(s, -1) {
		if _, ok := m[ref[1]]; !ok {
			missing = append(missing, ref[1])
		}
	}
	return missing
}

// expand replaces ${NAME} with the value of axis NAME; other text is kept.
func expand(s string, combo map[string]string) string {
	return varRe.ReplaceAllStringFunc(s, func(ref string) string {
		if v, ok := combo[ref[2:len(ref)-1]]; ok {
			return v
		}
		return ref
	})
}

func expandAll(env, combo map[string]string) map[string]string {
	if env == nil {
		return nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = expand(v, combo)
	}
	return out
}

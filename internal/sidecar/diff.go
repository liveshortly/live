package sidecar

import (
	"fmt"
	"strings"
)

const maxDiffLines = 120

// UnifiedDiff is unifiedDiff without the line counts, for agents that report
// an edit as old/new text (ACP diff content).
func UnifiedDiff(path, oldText, newText string) string {
	d, _, _ := unifiedDiff(path, oldText, newText)
	return d
}

// unifiedDiff renders an LCS-based line diff between old and new with +/-
// markers (the web viewer colorizes by line prefix). Returns the diff text
// and the added/removed line counts.
func unifiedDiff(path, oldText, newText string) (string, int, int) {
	oldLines := splitLines(oldText)
	newLines := splitLines(newText)

	var ops []string
	if len(oldLines)*len(newLines) > 250_000 {
		// Too big for DP — degrade to remove-all/add-all.
		for _, l := range oldLines {
			ops = append(ops, "-"+l)
		}
		for _, l := range newLines {
			ops = append(ops, "+"+l)
		}
	} else {
		ops = lcsOps(oldLines, newLines)
	}

	out := []string{"--- " + path, "+++ " + path}
	out = append(out, ops...)
	if len(out) > maxDiffLines {
		out = out[:maxDiffLines]
		out = append(out, fmt.Sprintf(`\ ... diff truncated at %d lines`, maxDiffLines))
	}

	added, removed := 0, 0
	for _, l := range out {
		if strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++") {
			added++
		} else if strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---") {
			removed++
		}
	}
	return strings.Join(out, "\n"), added, removed
}

// writeDiff renders a full new file as added lines (Write tool).
func writeDiff(path, content string) (string, int, int) {
	lines := splitLines(content)
	n := len(lines)
	if n > maxDiffLines {
		lines = lines[:maxDiffLines]
	}
	out := []string{"+++ " + path + " (new file)"}
	for _, l := range lines {
		out = append(out, "+"+l)
	}
	return strings.Join(out, "\n"), n, 0
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// lcsOps computes diff operations via longest-common-subsequence DP.
func lcsOps(a, b []string) []string {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var ops []string
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, " "+a[i])
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, "-"+a[i])
			i++
		default:
			ops = append(ops, "+"+b[j])
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, "-"+a[i])
	}
	for ; j < m; j++ {
		ops = append(ops, "+"+b[j])
	}
	return ops
}

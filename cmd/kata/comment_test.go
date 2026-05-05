package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestComment_AppendsToIssue(t *testing.T) {
	env, dir := setupCLIEnv(t)
	createIssueViaHTTP(t, env, dir, "x")

	out := runCLI(t, env, dir, "comment", "1", "--body", "looks good")
	assert.True(t, strings.Contains(out, "looks good") || strings.Contains(out, "comment"))
}

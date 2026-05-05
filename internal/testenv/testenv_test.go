package testenv_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wesm/kata/internal/testenv"
)

func TestEnv_BootsDaemonAndAnswersPing(t *testing.T) {
	env := testenv.New(t)
	body := env.RequireOK(t, "/api/v1/ping")
	assert.Contains(t, string(body), `"ok":true`)
}

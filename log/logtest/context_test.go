package logtest

import (
	"context"
	"testing"

	"github.com/containerd/containerd/log"
)

func TestLogTest(t *testing.T) {
	ctx := WithT(context.Background(), t)
	log.G(ctx).Info("log info")
}

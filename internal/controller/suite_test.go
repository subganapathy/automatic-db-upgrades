package controller_test

import (
	"fmt"
	"os"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

var testEnv *envtest.Environment

func TestMain(m *testing.M) {
	testEnv = &envtest.Environment{}

	fmt.Fprintln(os.Stderr, "starting envtest...")
	cfg, err := testEnv.Start()
	if err != nil {
		panic(err)
	}
	fmt.Fprintf(os.Stderr, "envtest started: host=%s\n", cfg.Host)

	code := m.Run()

	_ = testEnv.Stop()
	os.Exit(code)
}

func TestSmoke(t *testing.T) {
	t.Log("envtest started successfully")
}

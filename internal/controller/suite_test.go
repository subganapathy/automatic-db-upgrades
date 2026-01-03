package controller_test

import (
	"os"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

var testEnv *envtest.Environment

func TestMain(m *testing.M) {
	testEnv = &envtest.Environment{}

	_, err := testEnv.Start()
	if err != nil {
		panic(err)
	}

	code := m.Run()

	_ = testEnv.Stop()
	os.Exit(code)
}

func TestSmoke(t *testing.T) {
	t.Log("envtest started successfully")
}

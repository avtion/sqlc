package main

import (
	"os"
	"testing"

	"github.com/kyleconroy/sqlc/internal/cmd"
)

func Test_Generate(t *testing.T) {
	res, err := cmd.Generate(cmd.Env{ExperimentalFeatures: false}, ".", "sqlc.yaml", os.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	for name, source := range res {
		t.Log(name)
		t.Log(source)
	}
}

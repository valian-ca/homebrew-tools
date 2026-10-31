package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestHelpDescribesMySQLReconciliation(t *testing.T) {
	root := newRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"erases the databases of a gone lane", "retries every\n5 minutes", "never wakes nor stops the MySQL server"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("lanesd --help lacks %q:\n%s", want, out.String())
		}
	}
}

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
	help := strings.Join(strings.Fields(out.String()), " ")
	for _, want := range []string{"erases the databases of a gone lane", "or those recorded at registration", "retries every 5 minutes", "never wakes nor stops the MySQL server"} {
		if !strings.Contains(help, want) {
			t.Errorf("lanesd --help lacks %q:\n%s", want, out.String())
		}
	}
}

package main

import (
	"os"
	"strconv"
	"time"
)

func main() {
	seconds := 600
	if len(os.Args) > 1 {
		if n, err := strconv.Atoi(os.Args[1]); err == nil {
			seconds = n
		}
	}
	time.Sleep(time.Duration(seconds) * time.Second)
}

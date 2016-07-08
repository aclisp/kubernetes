package main

import (
	"fmt"
	"testing"
)

func TestWriter(t *testing.T) {
	MaxNumber = 5
	b := NewFlushSyncWriter()
	for i := uint64(0); i <= MaxSize; i++ {
		fmt.Fprintf(b, "test %d\n", i)
	}
	b.Flush()
	b.Sync()
}

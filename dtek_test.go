package main

import (
	"fmt"
	"testing"
)

func TestDtekFetch(t *testing.T) {
	client := NewDtekClient("м. Підгороднє", "вул. Сагайдачного Петра", "1")
	shutdown, err := client.FetchShutdowns()
	if err != nil {
		t.Fatalf("FetchShutdowns error: %v", err)
	}
	if shutdown == nil {
		fmt.Println("No shutdown scheduled for this house")
		return
	}
	fmt.Printf("Shutdown: %s → %s (%s)\n", shutdown.StartDate, shutdown.EndDate, shutdown.SubType)
}

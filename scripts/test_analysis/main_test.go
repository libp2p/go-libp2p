package main

// These tests are just useful for testing the test runner itself.

// func TestFlaky(t *testing.T) {
// 	if rand.Intn(2) == 0 {
// 		t.Fatal("flaky test")
// 	}
// }

// func TestFailsRace(t *testing.T) {
// 	c := make(chan bool)
// 	m := make(map[string]string)
// 	go func() {
// 		m["1"] = "a" // First conflicting access.
// 		c <- true
// 	}()
// 	m["2"] = "b" // Second conflicting access.
// 	<-c
// 	for k, v := range m {
// 		fmt.Println(k, v)
// 	}
// }

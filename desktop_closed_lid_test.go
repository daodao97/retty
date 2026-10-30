package main

import "testing"

func TestSleepDisabledReadsPmset(t *testing.T) {
	out := "System-wide power settings:\n SleepDisabled\t\t1\nCurrently in use:\n sleep                0\n"
	if on, ok := sleepDisabledIn(out); !on || !ok {
		t.Fatal(on, ok)
	}
	if on, ok := sleepDisabledIn(" SleepDisabled\t\t0\n"); on || !ok {
		t.Fatal(on, ok)
	}
	if _, ok := sleepDisabledIn(" sleep 1\n"); ok {
		t.Fatal("found SleepDisabled where there is none")
	}
}

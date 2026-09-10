package device

import (
	"context"
	"errors"
	"testing"
)

func TestGatesSerializeOnlyTheSameDevice(t *testing.T) {
	gates := NewGates()
	release, err := gates.Acquire(context.Background(), "runner/one")
	if err != nil {
		t.Fatal(err)
	}
	if other, err := gates.Acquire(context.Background(), "runner/two"); err != nil {
		t.Fatal(err)
	} else {
		other()
	}
	blocked, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := gates.Acquire(blocked, "runner/one"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acquire error = %v", err)
	}
	release()
	release()
	if _, err := gates.Acquire(context.Background(), "runner/one"); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceErrorsDescribeTheirIdentity(t *testing.T) {
	if (UnknownDeviceError{"one"}).Error() != `unknown device "one"` || (DisabledDeviceError{"two"}).Error() != `device "two" is disabled` || (DuplicateDeviceError{"three"}).Error() != `duplicate device "three"` {
		t.Fatal("device errors lost their identity")
	}
}

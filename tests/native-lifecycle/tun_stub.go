// Test-only dependency for executing the unmodified StartTUN/StopTun functions
// on the host. The OS constructor is controllable; this is not an OHOS device.
package tun

type Listener struct{ CloseFn func() error }

func (l *Listener) Close() error {
	if l.CloseFn != nil {
		return l.CloseFn()
	}
	return nil
}

var StartFn func(int, string, string, []string) (*Listener, error)

func Start(fd int, device, stack string, dns []string) (*Listener, error) {
	return StartFn(fd, device, stack, dns)
}

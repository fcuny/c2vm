//go:build darwin

package vz

import (
	"bytes"
	"errors"
	"log"
	"os"
	"strings"
	"testing"

	"fcuny.net/c2vm/internal/guest"
)

func TestBoots(t *testing.T) {
	errOther := errors.New("other")
	for _, tt := range []struct {
		name string
		// results are what each boot returns, in order.
		results  []error
		wantErr  error // nil, errOther, or errBootStuck for giving up
		wantRuns int
	}{
		{"first boot", []error{nil}, nil, 1},
		{"restarted", []error{errBootStuck, errBootStuck, nil}, nil, 3},
		{"gives up", []error{errBootStuck, errBootStuck, errBootStuck}, errBootStuck, 3},
		// Only a stuck boot is restarted: anything else may have run
		// the command.
		{"other error", []error{errOther}, errOther, 1},
		{"other error after a restart", []error{errBootStuck, errOther}, errOther, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(os.Stderr) })

			runs := 0
			code, err := boots(3, func(n int) (int, error) {
				if n != runs {
					t.Errorf("boot %d called as boot %d", runs, n)
				}
				runs++
				if runs > len(tt.results) {
					t.Fatalf("boot called %d times, want %d", runs, len(tt.results))
				}
				if err := tt.results[runs-1]; err != nil {
					return 0, err
				}
				return 7, nil
			})
			if runs != tt.wantRuns {
				t.Errorf("%d boots, want %d", runs, tt.wantRuns)
			}
			switch tt.wantErr {
			case nil:
				if err != nil || code != 7 {
					t.Errorf("boots = %d, %v, want 7, nil", code, err)
				}
			case errBootStuck:
				// The last boot's error, which says it was stuck, isn't
				// the one to report: c2vm gave up on restarting.
				if err == nil || errors.Is(err, errBootStuck) || !strings.Contains(err.Error(), "3 times") {
					t.Errorf("boots = %v, want an error saying the vm didn't start 3 times", err)
				}
			default:
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("boots = %v, want %v", err, tt.wantErr)
				}
			}
			// Giving up isn't a restart.
			restarts := tt.wantRuns - 1
			if got := strings.Count(logs.String(), "restarting the vm"); got != restarts {
				t.Errorf("%d restarts logged, want %d:\n%s", got, restarts, logs.String())
			}
		})
	}
}

func TestShutdown(t *testing.T) {
	// A guest that reboots is restarted, so the VM would never stop.
	if got := New().Shutdown(); got != guest.ShutdownPowerOff {
		t.Errorf("Shutdown() = %q, want %q", got, guest.ShutdownPowerOff)
	}
}

func TestCommandLine(t *testing.T) {
	args := strings.Fields(commandLine)
	// A panic that rebooted the guest would restart the VM and run the
	// command again: the host stops it instead.
	// The kernel's messages, panics included, go to their own console.
	for _, want := range []string{"console=hvc1", "ip=dhcp", "oops=panic", "panic=0"} {
		found := false
		for _, arg := range args {
			found = found || arg == want
		}
		if !found {
			t.Errorf("command line %q is missing %s", commandLine, want)
		}
	}
	for _, arg := range args {
		if arg == "console="+strings.TrimPrefix(guest.CommandConsole, "/dev/") {
			t.Errorf("command line %q puts kernel messages on the command's console", commandLine)
		}
		if strings.HasPrefix(arg, "init=") || strings.HasPrefix(arg, "root=") {
			t.Errorf("command line %q sets %s, but the initramfs' init mounts the image", commandLine, arg)
		}
	}
}

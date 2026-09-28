//go:build linux && (arm64 || amd64)

package input

import (
	"encoding/binary"
	"fmt"
	"syscall"
	"unsafe"
)

// Linux asm-generic ioctl encoding, shared by the supported 64-bit targets.
const (
	eviocGrab    = 0x40044590
	eviocKey     = 0x80604518 // EVIOCGKEY(96), KEY_MAX bitmap
	eviocKeyBits = 0x80604521 // EVIOCGBIT(EV_KEY, 96)
)

type evdev struct{ fd int }

func Open(path string) (*Monitor, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open touchscreen %s: %w", path, err)
	}
	d := &evdev{fd: fd}
	var bits [96]byte
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), eviocKeyBits, uintptr(unsafe.Pointer(&bits[0])))
	if errno != 0 || bits[btnTouch/8]&(1<<(btnTouch%8)) == 0 {
		d.Close()
		return nil, fmt.Errorf("touchscreen must expose BTN_TOUCH contact state (ioctl errno %d)", errno)
	}
	return newMonitor(d)
}

func (d *evdev) TouchDown() (bool, error) {
	var bits [96]byte
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(d.fd), eviocKey, uintptr(unsafe.Pointer(&bits[0])))
	if errno != 0 {
		return false, fmt.Errorf("query touch contact: %w", errno)
	}
	return bits[btnTouch/8]&(1<<(btnTouch%8)) != 0, nil
}
func (d *evdev) Grab(grab bool) error {
	var value uintptr
	if grab {
		value = 1
	}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(d.fd), eviocGrab, value)
	if errno != 0 {
		return errno
	}
	return nil
}
func (d *evdev) Close() error { return syscall.Close(d.fd) }
func (d *evdev) Read() ([]event, error) {
	var events []event
	var buf [24 * 256]byte // struct input_event on Linux amd64/arm64
	for {
		n, err := syscall.Read(d.fd, buf[:])
		if err == syscall.EAGAIN {
			return events, nil
		}
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read touchscreen: %w", err)
		}
		if n == 0 || n%24 != 0 {
			return nil, fmt.Errorf("touchscreen returned an incomplete event stream")
		}
		for offset := 0; offset < n; offset += 24 {
			b := buf[offset : offset+24]
			events = append(events, event{Type: binary.LittleEndian.Uint16(b[16:18]), Code: binary.LittleEndian.Uint16(b[18:20]), Value: int32(binary.LittleEndian.Uint32(b[20:24]))})
		}
		if len(events) > 65536 {
			return nil, fmt.Errorf("touchscreen event backlog is too large")
		}
	}
}

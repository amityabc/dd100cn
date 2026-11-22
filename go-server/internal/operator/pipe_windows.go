package operator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"time"
	"unsafe"

	"github.com/google/uuid"
	"golang.org/x/sys/windows"
)

type pipe struct {
	handle windows.Handle
	ctx    context.Context
}

// The handle stays open until the outstanding Win32 operation acknowledges cancellation.
func (p pipe) io(start func(*windows.Overlapped, *uint32) error) (int, error) {
	if err := p.ctx.Err(); err != nil {
		return 0, err
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(event)
	op := windows.Overlapped{HEvent: event}
	var count uint32
	err = start(&op, &count)
	if err != windows.ERROR_IO_PENDING {
		return int(count), err
	}
	done := make(chan struct{})
	stop := context.AfterFunc(p.ctx, func() { _ = windows.CancelIoEx(p.handle, &op); close(done) })
	err = windows.GetOverlappedResult(p.handle, &op, &count, true)
	if !stop() {
		<-done
	}
	if err == windows.ERROR_OPERATION_ABORTED && p.ctx.Err() != nil {
		err = p.ctx.Err()
	}
	return int(count), err
}

func (p pipe) Read(b []byte) (int, error) {
	n, err := p.io(func(op *windows.Overlapped, n *uint32) error { return windows.ReadFile(p.handle, b, n, op) })
	runtime.KeepAlive(b)
	if err == windows.ERROR_BROKEN_PIPE || err == nil && n == 0 && len(b) > 0 {
		err = io.EOF
	}
	return n, err
}
func (p pipe) Write(b []byte) (int, error) {
	n, err := p.io(func(op *windows.Overlapped, n *uint32) error { return windows.WriteFile(p.handle, b, n, op) })
	runtime.KeepAlive(b)
	return n, err
}

func pipePath(root string) (*uint16, error) {
	name, err := PipeName(root)
	if err != nil {
		return nil, err
	}
	return windows.UTF16PtrFromString("\\\\.\\pipe\\" + name)
}

func Serve(ctx context.Context, root string, process func(context.Context, Intent) Reply) error {
	path, err := pipePath(root)
	if err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sid := user.User.Sid.String()
	sd, err := windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;;GA;;;" + sid + ")")
	if err != nil {
		return err
	}
	sa := windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	h, err := windows.CreateNamedPipe(path, windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_REJECT_REMOTE_CLIENTS, 1, 8192, 8192, 5000, &sa)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	for ctx.Err() == nil {
		p := pipe{h, ctx}
		_, err = p.io(func(op *windows.Overlapped, _ *uint32) error { return windows.ConnectNamedPipe(h, op) })
		if err == nil || err == windows.ERROR_PIPE_CONNECTED {
			request, cancel := context.WithTimeout(ctx, 15*time.Second)
			p.ctx = request
			body, err := ReadFrame(p)
			var reply Reply
			if err == nil {
				intent, err := Parse(body)
				if err != nil {
					reply = Message("Rejected", uuid.Nil, err.Error())
				} else {
					reply = process(request, intent)
				}
			} else {
				reply = Message("Rejected", uuid.Nil, err.Error())
			}
			if data, err := json.Marshal(reply); err == nil && WriteFrame(p, data) == nil {
				// The original operator closes its client handle after reading the single response.
				// Wait for that close before DisconnectNamedPipe discards an unread reply.
				var end [1]byte
				_, _ = p.Read(end[:])
			}
			cancel()
		}
		if err != nil && err != windows.ERROR_PIPE_CONNECTED && err != windows.ERROR_NO_DATA && ctx.Err() == nil {
			return err
		}
		_ = windows.DisconnectNamedPipe(h)
	}
	return nil
}

func Call(ctx context.Context, root string, body []byte) (Reply, error) {
	path, err := pipePath(root)
	if err != nil {
		return Reply{}, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return Reply{}, err
	}
	timeout := 5 * time.Second
	if i, e := Parse(body); e == nil {
		if _, ok := i.Command.(Mail); ok {
			timeout = time.Second
		}
	}
	connect, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var h windows.Handle
	for {
		h, err = windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL, 0, nil, windows.OPEN_EXISTING,
			windows.FILE_FLAG_OVERLAPPED|windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
		if err == nil {
			break
		}
		if err != windows.ERROR_FILE_NOT_FOUND && err != windows.ERROR_PIPE_BUSY {
			return Reply{}, errors.Join(ErrNotConnected, err)
		}
		select {
		case <-connect.Done():
			return Reply{}, errors.Join(ErrNotConnected, connect.Err())
		case <-time.After(35 * time.Millisecond):
		}
	}
	defer windows.CloseHandle(h)
	sd, err := windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return Reply{}, err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return Reply{}, err
	}
	if owner == nil || !user.User.Sid.Equals(owner) {
		return Reply{}, errors.New("operator pipe owner differs from current user")
	}
	return Exchange(pipe{h, ctx}, body)
}

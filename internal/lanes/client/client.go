package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/lanes/paths"
	"github.com/valian-ca/homebrew-tools/internal/lanes/proto"
)

var ErrDaemonDown = errors.New("lanesd is not running — run: brew services start lanes")

func Call(ctx context.Context, req proto.Request) (proto.Response, error) {
	d := net.Dialer{Timeout: time.Second}
	conn, err := d.DialContext(ctx, "unix", paths.Socket())
	if err != nil {
		return proto.Response{}, ErrDaemonDown
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return proto.Response{}, fmt.Errorf("lanesd: %w", err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		if ctx.Err() != nil {
			return proto.Response{}, fmt.Errorf("lanesd: %w", ctx.Err())
		}
		return proto.Response{}, fmt.Errorf("lanesd: %w", err)
	}
	var resp proto.Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return proto.Response{}, fmt.Errorf("lanesd: bad reply: %w", err)
	}
	if !resp.OK {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

func Ping(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := Call(ctx, proto.Request{Op: proto.OpPing})
	return err == nil
}

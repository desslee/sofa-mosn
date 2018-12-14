package keepalive

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alipay/sofa-mosn/pkg/api/v2"
	"github.com/alipay/sofa-mosn/pkg/protocol"
	"github.com/alipay/sofa-mosn/pkg/protocol/rpc/sofarpc"
	"github.com/alipay/sofa-mosn/pkg/stream"
	_ "github.com/alipay/sofa-mosn/pkg/stream/sofarpc"
	"github.com/alipay/sofa-mosn/pkg/types"
	"github.com/alipay/sofa-mosn/pkg/upstream/cluster"
)

type stats struct {
	success uint32
	timeout uint32
}

func (s *stats) Record(status types.KeepAliveStatus) {
	switch status {
	case types.KeepAliveSuccess:
		atomic.AddUint32(&s.success, 1)
	case types.KeepAliveTimeout:
		atomic.AddUint32(&s.timeout, 1)
	}
}

// use bolt v1 to test keep alive
type testCase struct {
	KeepAlive *sofaRPCKeepAlive
	Server    *mockServer
}

func newTestCase(t *testing.T, srvTimeout, keepTimeout time.Duration, thres uint32) *testCase {
	// start a mock server
	srv, err := newMockServer(srvTimeout)
	if err != nil {
		t.Fatal(err)
	}
	srv.GoServe()
	defer srv.Close()
	// make a connection to server
	info := &mockClusterInfo{
		name:  "test",
		limit: 1024,
	}
	cfg := v2.Host{
		HostConfig: v2.HostConfig{
			Address:    srv.AddrString(),
			TLSDisable: true, // ignore tls, for mock is nil
		},
	}
	host := cluster.NewHost(cfg, info)
	ctx := context.Background()
	conn := host.CreateConnection(ctx)
	if err := conn.Connection.Connect(true); err != nil {
		t.Fatalf("create conenction failed", err)
	}
	codec := stream.NewCodecClient(ctx, protocol.SofaRPC, conn.Connection, host)
	if codec == nil {
		t.Fatal("codec is nil")
	}
	// start a keep alive
	keepAlive := NewSofaRPCKeepAlive(codec, sofarpc.PROTOCOL_CODE_V1, keepTimeout, thres)
	go keepAlive.Start()
	return &testCase{
		KeepAlive: keepAlive.(*sofaRPCKeepAlive),
		Server:    srv,
	}

}

func TestKeepAlive(t *testing.T) {
	tc := newTestCase(t, 0, time.Second, 6)
	stats := &stats{}
	tc.KeepAlive.AddCallback(stats.Record)
	// test concurrency
	for i := 0; i < 5; i++ {
		go tc.KeepAlive.SendKeepAlive()
	}
	// wait response
	time.Sleep(2 * time.Second)
	if stats.success != 5 {
		t.Error("keep alive handle success not enough", stats)
	}
}

func TestKeepAliveTimeout(t *testing.T) {
	tc := newTestCase(t, 50*time.Millisecond, 10*time.Millisecond, 6)
	stats := &stats{}
	tc.KeepAlive.AddCallback(stats.Record)
	// after 6 times, the connection will be closed
	for i := 0; i < 10; i++ {
		tc.KeepAlive.SendKeepAlive()
		time.Sleep(80 * time.Millisecond)
	}
	// wait all response
	time.Sleep(time.Second)
	if stats.timeout != 10 {
		t.Error("keep alive handle failure not enough", stats)
	}
}

func TestKeepAliveTimeoutAndSuccess(t *testing.T) {
	tc := newTestCase(t, 150*time.Millisecond, 20*time.Millisecond, 6)
	stats := &stats{}
	tc.KeepAlive.AddCallback(stats.Record)
	// 5 times timeout, will not close the connection
	for i := 0; i < 5; i++ {
		tc.KeepAlive.SendKeepAlive()
		time.Sleep(200 * time.Millisecond)
	}
	// set no delay, will not timeour
	tc.Server.delay = 0
	tc.KeepAlive.SendKeepAlive()
	// wait response
	time.Sleep(time.Second)
	if stats.success != 1 || stats.timeout != 5 {
		t.Error("keep alive handle status not expected", stats)
	}
	if tc.KeepAlive.timeoutCount != 0 {
		t.Error("timeout count not reset by success")
	}

}
package client_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	pbv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/pb"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/proto"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/util"

	"github.com/stretchr/testify/require"
)

func TestReservationFailures(t *testing.T) {
	type testcase struct {
		name          string
		streamHandler network.StreamHandler
		err           string
		status        pbv2.Status
	}
	testcases := []testcase{
		{
			name:          "unsupported protocol",
			streamHandler: nil,
			err:           "protocols not supported",
		},
		{
			name: "wrong message type",
			streamHandler: func(s network.Stream) {
				util.NewDelimitedWriter(s).WriteMsg(&pbv2.HopMessage{
					Type: pbv2.HopMessage_RESERVE.Enum(),
				})
			},
			err:    "unexpected relay response: not a status message",
			status: pbv2.Status_MALFORMED_MESSAGE,
		},
		{
			name: "unknown status",
			streamHandler: func(s network.Stream) {
				status := pbv2.Status(1337)
				util.NewDelimitedWriter(s).WriteMsg(&pbv2.HopMessage{
					Type:   pbv2.HopMessage_STATUS.Enum(),
					Status: &status,
				})
			},
			err:    "reservation failed",
			status: pbv2.Status(1337),
		},
		{
			name: "invalid time",
			streamHandler: func(s network.Stream) {
				status := pbv2.Status_OK
				expire := uint64(math.MaxUint64)
				util.NewDelimitedWriter(s).WriteMsg(&pbv2.HopMessage{
					Type:        pbv2.HopMessage_STATUS.Enum(),
					Status:      &status,
					Reservation: &pbv2.Reservation{Expire: &expire},
				})
			},
			err:    "received reservation with expiration date in the past",
			status: pbv2.Status_MALFORMED_MESSAGE,
		},
		{
			name: "invalid voucher",
			streamHandler: func(s network.Stream) {
				status := pbv2.Status_OK
				expire := uint64(time.Now().Add(time.Hour).UnixNano())
				util.NewDelimitedWriter(s).WriteMsg(&pbv2.HopMessage{
					Type:   pbv2.HopMessage_STATUS.Enum(),
					Status: &status,
					Reservation: &pbv2.Reservation{
						Expire:  &expire,
						Voucher: []byte("foobar"),
					},
				})
			},
			err:    "error consuming voucher envelope: failed when unmarshalling the envelope",
			status: pbv2.Status_MALFORMED_MESSAGE,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			host, err := libp2p.New(libp2p.ResourceManager(&network.NullResourceManager{}))
			require.NoError(t, err)
			defer host.Close()
			if tc.streamHandler != nil {
				host.SetStreamHandler(proto.ProtoIDv2Hop, tc.streamHandler)
			}

			cl, err := libp2p.New(libp2p.ResourceManager(&network.NullResourceManager{}))
			require.NoError(t, err)
			defer cl.Close()
			_, err = client.Reserve(context.Background(), cl, peer.AddrInfo{ID: host.ID(), Addrs: host.Addrs()})
			if tc.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.err)
				if tc.status != 0 {
					var re client.ReservationError
					if !errors.As(err, &re) {
						t.Errorf("expected error to be of type %T", re)
					}
					if re.Status != tc.status {
						t.Errorf("expected status %d got %d", tc.status, re.Status)
					}
				}
			}
		})
	}
}

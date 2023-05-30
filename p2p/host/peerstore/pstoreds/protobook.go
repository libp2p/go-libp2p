package pstoreds

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
	pstore "github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
)

type protoSegment struct {
	sync.RWMutex
}

type protoSegments [256]*protoSegment

func (s *protoSegments) get(p peer.ID) *protoSegment {
	return s[byte(p[len(p)-1])]
}

var errTooManyProtocols = errors.New("too many protocols")

type ProtoBookOption func(*dsProtoBook) error

func WithMaxProtocols(num int) ProtoBookOption {
	return func(pb *dsProtoBook) error {
		pb.maxProtos = num
		return nil
	}
}

type dsProtoBook struct {
	segments  protoSegments
	meta      pstore.PeerMetadata
	maxProtos int
}

var _ pstore.ProtoBook = (*dsProtoBook)(nil)

func NewProtoBook(meta pstore.PeerMetadata, opts ...ProtoBookOption) (*dsProtoBook, error) {
	pb := &dsProtoBook{
		meta: meta,
		segments: func() (ret protoSegments) {
			for i := range ret {
				ret[i] = &protoSegment{}
			}
			return ret
		}(),
		maxProtos: 1024,
	}

	for _, opt := range opts {
		if err := opt(pb); err != nil {
			return nil, err
		}
	}
	return pb, nil
}

func (pb *dsProtoBook) SetProtocols(ctx context.Context, p peer.ID, protos ...protocol.ID) error {
	if len(protos) > pb.maxProtos {
		return errTooManyProtocols
	}

	protomap := make(map[protocol.ID]struct{}, len(protos))
	for _, proto := range protos {
		protomap[proto] = struct{}{}
	}

	s := pb.segments.get(p)
	s.Lock()
	defer s.Unlock()

	return pb.meta.Put(ctx, p, "protocols", protomap)
}

func (pb *dsProtoBook) AddProtocols(ctx context.Context, p peer.ID, protos ...protocol.ID) error {
	s := pb.segments.get(p)
	s.Lock()
	defer s.Unlock()

	pmap, err := pb.getProtocolMap(ctx, p)
	if err != nil {
		return err
	}
	if len(pmap)+len(protos) > pb.maxProtos {
		return errTooManyProtocols
	}

	for _, proto := range protos {
		pmap[proto] = struct{}{}
	}

	return pb.meta.Put(ctx, p, "protocols", pmap)
}

func (pb *dsProtoBook) GetProtocols(ctx context.Context, p peer.ID) ([]protocol.ID, error) {
	s := pb.segments.get(p)
	s.RLock()
	defer s.RUnlock()

	pmap, err := pb.getProtocolMap(ctx, p)
	if err != nil {
		return nil, err
	}

	res := make([]protocol.ID, 0, len(pmap))
	for proto := range pmap {
		res = append(res, proto)
	}

	return res, nil
}

func (pb *dsProtoBook) SupportsProtocols(ctx context.Context, p peer.ID, protos ...protocol.ID) ([]protocol.ID, error) {
	s := pb.segments.get(p)
	s.RLock()
	defer s.RUnlock()

	pmap, err := pb.getProtocolMap(ctx, p)
	if err != nil {
		return nil, err
	}

	res := make([]protocol.ID, 0, len(protos))
	for _, proto := range protos {
		if _, ok := pmap[proto]; ok {
			res = append(res, proto)
		}
	}

	return res, nil
}

func (pb *dsProtoBook) FirstSupportedProtocol(ctx context.Context, p peer.ID, protos ...protocol.ID) (protocol.ID, error) {
	s := pb.segments.get(p)
	s.RLock()
	defer s.RUnlock()

	pmap, err := pb.getProtocolMap(ctx, p)
	if err != nil {
		return "", err
	}
	for _, proto := range protos {
		if _, ok := pmap[proto]; ok {
			return proto, nil
		}
	}

	return "", nil
}

func (pb *dsProtoBook) RemoveProtocols(ctx context.Context, p peer.ID, protos ...protocol.ID) error {
	s := pb.segments.get(p)
	s.Lock()
	defer s.Unlock()

	pmap, err := pb.getProtocolMap(ctx, p)
	if err != nil {
		return err
	}

	if len(pmap) == 0 {
		// nothing to do.
		return nil
	}

	for _, proto := range protos {
		delete(pmap, proto)
	}

	return pb.meta.Put(ctx, p, "protocols", pmap)
}

func (pb *dsProtoBook) getProtocolMap(ctx context.Context, p peer.ID) (map[protocol.ID]struct{}, error) {
	iprotomap, err := pb.meta.Get(ctx, p, "protocols")
	switch err {
	default:
		return nil, err
	case pstore.ErrNotFound:
		return make(map[protocol.ID]struct{}), nil
	case nil:
		cast, ok := iprotomap.(map[protocol.ID]struct{})
		if !ok {
			return nil, fmt.Errorf("stored protocol set was not a map")
		}

		return cast, nil
	}
}

func (pb *dsProtoBook) RemovePeer(ctx context.Context, p peer.ID) {
	pb.meta.RemovePeer(ctx, p)
}

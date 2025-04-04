package sniff

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/netip"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
)

func TLSClientHello(ctx context.Context, metadata *adapter.InboundContext, reader io.Reader) error {
	var clientHello *tls.ClientHelloInfo
	err := tls.Server(bufio.NewReadOnlyConn(reader), &tls.Config{
		GetConfigForClient: func(argHello *tls.ClientHelloInfo) (*tls.Config, error) {
			clientHello = argHello
			return nil, nil
		},
	}).HandshakeContext(ctx)
	if clientHello != nil {
		metadata.Protocol = C.ProtocolTLS
		if _, err = netip.ParseAddr(clientHello.ServerName); err != nil {
			metadata.SniffHost = clientHello.ServerName
		}
		return nil
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return E.Cause1(ErrNeedMoreData, err)
	} else {
		return err
	}
}

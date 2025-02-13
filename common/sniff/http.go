package sniff

import (
	std_bufio "bufio"
	"context"
	"io"
	"net/netip"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/protocol/http"
)

func HTTPHost(_ context.Context, metadata *adapter.InboundContext, reader io.Reader) error {
	request, err := http.ReadRequest(std_bufio.NewReader(reader))
	if err != nil {
		return err
	}
	metadata.Protocol = C.ProtocolHTTP
	host := M.ParseSocksaddr(request.Host).AddrString()
	if _, err = netip.ParseAddr(host); err != nil {
		metadata.SniffHost = host
	}
	return nil
}

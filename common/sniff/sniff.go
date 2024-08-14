package sniff

import (
	"bytes"
	"context"
	"io"
	"net"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common/buf"
	E "github.com/sagernet/sing/common/exceptions"
)

type (
	StreamSniffer = func(ctx context.Context, metadata *adapter.InboundContext, reader io.Reader) error
	PacketSniffer = func(ctx context.Context, metadata *adapter.InboundContext, packet []byte) error
)

func Skip(metadata *adapter.InboundContext) bool {
	// skip server first protocols
	switch metadata.Destination.Port {
	case 25, 465, 587:
		// SMTP
		return true
	case 143, 993:
		// IMAP
		return true
	case 110, 995:
		// POP3
		return true
	}
	return false
}

func PeekStream(ctx context.Context, metadata *adapter.InboundContext, conn net.Conn, buffer *buf.Buffer, timeout time.Duration, sniffers ...StreamSniffer) error {
	if timeout == 0 {
		timeout = C.ReadPayloadTimeout
	}
	deadline := time.Now().Add(timeout)
	var errors []error
	for i := 0; ; i++ {
		err := conn.SetReadDeadline(deadline)
		if err != nil {
			return E.Cause(err, "set read deadline")
		}
		_, err = buffer.ReadOnceFrom(conn)
		_ = conn.SetReadDeadline(time.Time{})
		if err != nil {
			if i > 0 {
				break
			}
			return E.Cause(err, "read payload")
		}
		errors = nil
		snifferCounts := len(sniffers)
		errorsChan := make(chan error, len(sniffers))
		fastClose, cancel := context.WithCancel(ctx)
		for _, sniffer := range sniffers {
			go func(sniffer StreamSniffer) {
				errorsChan <- sniffer(fastClose, metadata, bytes.NewReader(buffer.Bytes()))
			}(sniffer)
		}
		for i := 0; i < snifferCounts; i++ {
			err := <-errorsChan
			if err == nil {
				go func(index int) {
					for i := index; i < snifferCounts; i++ {
						<-errorsChan
					}
					close(errorsChan)
				}(i + 1)
				cancel()
				return nil
			}
			errors = append(errors, err)
		}
		close(errorsChan)
		cancel()
	}
	return E.Errors(errors...)
}

func PeekPacket(ctx context.Context, metadata *adapter.InboundContext, packet []byte, sniffers ...PacketSniffer) error {
	var errors []error
	snifferCounts := len(sniffers)
	errorsChan := make(chan error, len(sniffers))
	fastClose, cancel := context.WithCancel(ctx)
	defer cancel()
	for _, sniffer := range sniffers {
		go func(sniffer PacketSniffer) {
			errorsChan <- sniffer(fastClose, metadata, packet)
		}(sniffer)
	}
	for i := 0; i < snifferCounts; i++ {
		err := <-errorsChan
		if err == nil {
			go func(index int) {
				for i := index; i < snifferCounts; i++ {
					<-errorsChan
				}
				close(errorsChan)
			}(i + 1)
			return nil
		}
		errors = append(errors, err)
	}
	close(errorsChan)
	return E.Errors(errors...)
}

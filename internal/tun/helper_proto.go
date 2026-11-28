package tun

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

const (
	helperFrameHello     byte = 1
	helperFrameReady     byte = 2
	helperFramePacket    byte = 3
	helperFrameAddBypass byte = 4
	helperFramePing      byte = 5
	helperFramePong      byte = 6
	helperFrameStop      byte = 7
	helperFrameError     byte = 8

	maxHelperFrameSize = 128 * 1024
)

type helperHelloMsg struct {
	Token string `json:"token"`
}

type helperReadyMsg struct {
	DeviceName    string `json:"deviceName"`
	MTU           int    `json:"mtu"`
	OutboundIface string `json:"outboundIface"`
	Gateway       string `json:"gateway"`
	RouteCount    int    `json:"routeCount"`
}

type helperBypassMsg struct {
	IP string `json:"ip"`
}

type helperErrorMsg struct {
	Error string `json:"error"`
}

func writeHelperFrame(w io.Writer, typ byte, payload []byte) error {
	if len(payload) > maxHelperFrameSize {
		return fmt.Errorf("帧长度超限: %d", len(payload))
	}
	var hdr [5]byte
	hdr[0] = typ
	binary.BigEndian.PutUint32(hdr[1:5], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

func writeHelperJSON(w io.Writer, typ byte, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeHelperFrame(w, typ, b)
}

func readHelperFrame(r io.Reader, scratch []byte) (typ byte, payload []byte, err error) {
	var hdr [5]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	typ = hdr[0]
	n := int(binary.BigEndian.Uint32(hdr[1:5]))
	if n < 0 || n > maxHelperFrameSize {
		return 0, nil, fmt.Errorf("收到非法帧长度: %d", n)
	}
	if n == 0 {
		return typ, nil, nil
	}
	if len(scratch) >= n {
		payload = scratch[:n]
	} else {
		payload = make([]byte, n)
	}
	if _, err = io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return typ, payload, nil
}

package tun

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// dialSOCKS5TCP 通过本地回环混合代理发起 SOCKS5 TCP CONNECT，支持域名（ATYP=0x03）与 IPv4/IPv6 地址。
func dialSOCKS5TCP(proxyAddr, username, password, targetHost string, targetPort int, timeout time.Duration) (net.Conn, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	conn, err := net.DialTimeout("tcp", proxyAddr, timeout)
	if err != nil {
		return nil, fmt.Errorf("连接本地代理失败 (%s): %w", proxyAddr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if err := socks5Handshake(conn, username, password); err != nil {
		_ = conn.Close()
		return nil, err
	}
	req := buildSOCKS5AddrPacket(0x01, targetHost, targetPort)
	if _, err := conn.Write(req); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("发送 SOCKS5 CONNECT 失败: %w", err)
	}
	if _, _, err := readSOCKS5Reply(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

// socks5UDPSession 封装通过本地混合代理建立的 SOCKS5 UDP ASSOCIATE 通道。
type socks5UDPSession struct {
	ctrlConn  net.Conn
	udpConn   *net.UDPConn
	relayAddr *net.UDPAddr
}

func openSOCKS5UDP(proxyAddr, username, password string, timeout time.Duration) (*socks5UDPSession, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctrl, err := net.DialTimeout("tcp", proxyAddr, timeout)
	if err != nil {
		return nil, fmt.Errorf("连接本地代理控制通道失败 (%s): %w", proxyAddr, err)
	}
	_ = ctrl.SetDeadline(time.Now().Add(timeout))
	if err := socks5Handshake(ctrl, username, password); err != nil {
		_ = ctrl.Close()
		return nil, err
	}
	req := buildSOCKS5AddrPacket(0x03, "0.0.0.0", 0)
	if _, err := ctrl.Write(req); err != nil {
		_ = ctrl.Close()
		return nil, fmt.Errorf("发送 SOCKS5 UDP ASSOCIATE 失败: %w", err)
	}
	bndHost, bndPort, err := readSOCKS5Reply(ctrl)
	if err != nil {
		_ = ctrl.Close()
		return nil, err
	}
	_ = ctrl.SetDeadline(time.Time{})

	if bndHost == "" || bndHost == "0.0.0.0" || bndHost == "::" {
		if h, _, splitErr := net.SplitHostPort(proxyAddr); splitErr == nil && h != "" {
			bndHost = h
		} else {
			bndHost = "127.0.0.1"
		}
	}
	relayAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(bndHost, strconv.Itoa(bndPort)))
	if err != nil {
		_ = ctrl.Close()
		return nil, err
	}
	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		_ = ctrl.Close()
		return nil, err
	}
	return &socks5UDPSession{
		ctrlConn:  ctrl,
		udpConn:   udpConn,
		relayAddr: relayAddr,
	}, nil
}

func (s *socks5UDPSession) Send(targetHost string, targetPort int, payload []byte) error {
	pkt := encodeSOCKS5UDPDatagram(targetHost, targetPort, payload)
	_, err := s.udpConn.WriteToUDP(pkt, s.relayAddr)
	return err
}

func (s *socks5UDPSession) Recv(buf []byte) (srcHost string, srcPort int, payload []byte, err error) {
	n, _, err := s.udpConn.ReadFromUDP(buf)
	if err != nil {
		return "", 0, nil, err
	}
	return decodeSOCKS5UDPDatagram(buf[:n])
}

func (s *socks5UDPSession) Close() error {
	if s == nil {
		return nil
	}
	var err error
	if s.udpConn != nil {
		err = s.udpConn.Close()
	}
	if s.ctrlConn != nil {
		_ = s.ctrlConn.Close()
	}
	return err
}

func socks5Handshake(conn net.Conn, username, password string) error {
	hasAuth := username != "" && password != ""
	var hello []byte
	if hasAuth {
		hello = []byte{0x05, 0x02, 0x00, 0x02}
	} else {
		hello = []byte{0x05, 0x01, 0x00}
	}
	if _, err := conn.Write(hello); err != nil {
		return fmt.Errorf("SOCKS5 握手发送失败: %w", err)
	}
	var resp [2]byte
	if _, err := io.ReadFull(conn, resp[:]); err != nil {
		return fmt.Errorf("SOCKS5 握手响应读取失败: %w", err)
	}
	if resp[0] != 0x05 {
		return fmt.Errorf("无效的 SOCKS5 版本: %d", resp[0])
	}
	switch resp[1] {
	case 0x00:
		return nil
	case 0x02:
		if !hasAuth {
			return errors.New("本地代理要求认证，但未配置用户名与密码")
		}
		auth := make([]byte, 0, 3+len(username)+len(password))
		auth = append(auth, 0x01, byte(len(username)))
		auth = append(auth, username...)
		auth = append(auth, byte(len(password)))
		auth = append(auth, password...)
		if _, err := conn.Write(auth); err != nil {
			return fmt.Errorf("发送 SOCKS5 用户名密码认证失败: %w", err)
		}
		var aresp [2]byte
		if _, err := io.ReadFull(conn, aresp[:]); err != nil {
			return fmt.Errorf("读取 SOCKS5 认证结果失败: %w", err)
		}
		if aresp[1] != 0x00 {
			return errors.New("SOCKS5 认证被本地代理拒绝")
		}
		return nil
	default:
		return fmt.Errorf("本地代理拒绝 SOCKS5 认证方式: 0x%02x", resp[1])
	}
}

func buildSOCKS5AddrPacket(cmd byte, host string, port int) []byte {
	buf := make([]byte, 0, 10+len(host))
	buf = append(buf, 0x05, cmd, 0x00)
	buf = appendSOCKS5Addr(buf, host, port)
	return buf
}

func appendSOCKS5Addr(buf []byte, host string, port int) []byte {
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			buf = append(buf, 0x01)
			buf = append(buf, v4...)
		} else {
			buf = append(buf, 0x04)
			buf = append(buf, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			host = host[:255]
		}
		buf = append(buf, 0x03, byte(len(host)))
		buf = append(buf, host...)
	}
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], uint16(port))
	buf = append(buf, p[:]...)
	return buf
}

func readSOCKS5Reply(conn net.Conn) (host string, port int, err error) {
	var hdr [4]byte
	if _, err = io.ReadFull(conn, hdr[:]); err != nil {
		return "", 0, fmt.Errorf("读取 SOCKS5 响应头失败: %w", err)
	}
	if hdr[0] != 0x05 {
		return "", 0, fmt.Errorf("无效 SOCKS5 响应版本: %d", hdr[0])
	}
	if hdr[1] != 0x00 {
		return "", 0, fmt.Errorf("SOCKS5 请求失败，错误码: 0x%02x", hdr[1])
	}
	switch hdr[3] {
	case 0x01:
		var b [4]byte
		if _, err = io.ReadFull(conn, b[:]); err != nil {
			return "", 0, err
		}
		host = net.IP(b[:]).String()
	case 0x03:
		var l [1]byte
		if _, err = io.ReadFull(conn, l[:]); err != nil {
			return "", 0, err
		}
		d := make([]byte, int(l[0]))
		if _, err = io.ReadFull(conn, d); err != nil {
			return "", 0, err
		}
		host = string(d)
	case 0x04:
		var b [16]byte
		if _, err = io.ReadFull(conn, b[:]); err != nil {
			return "", 0, err
		}
		host = net.IP(b[:]).String()
	default:
		return "", 0, fmt.Errorf("不支持的 SOCKS5 地址类型: 0x%02x", hdr[3])
	}
	var pb [2]byte
	if _, err = io.ReadFull(conn, pb[:]); err != nil {
		return "", 0, err
	}
	port = int(binary.BigEndian.Uint16(pb[:]))
	return host, port, nil
}

func encodeSOCKS5UDPDatagram(host string, port int, payload []byte) []byte {
	buf := make([]byte, 0, 16+len(host)+len(payload))
	buf = append(buf, 0x00, 0x00, 0x00) // RSV(2) + FRAG(1)
	buf = appendSOCKS5Addr(buf, host, port)
	buf = append(buf, payload...)
	return buf
}

func decodeSOCKS5UDPDatagram(pkt []byte) (host string, port int, payload []byte, err error) {
	if len(pkt) < 7 {
		return "", 0, nil, errors.New("SOCKS5 UDP 数据报过短")
	}
	if pkt[2] != 0x00 {
		return "", 0, nil, errors.New("暂不支持分片 SOCKS5 UDP 数据报")
	}
	off := 4
	switch pkt[3] {
	case 0x01:
		if len(pkt) < off+4+2 {
			return "", 0, nil, errors.New("SOCKS5 UDP IPv4 地址截断")
		}
		host = net.IP(pkt[off : off+4]).String()
		off += 4
	case 0x03:
		if len(pkt) < off+1 {
			return "", 0, nil, errors.New("SOCKS5 UDP 域名长度截断")
		}
		dlen := int(pkt[off])
		off++
		if len(pkt) < off+dlen+2 {
			return "", 0, nil, errors.New("SOCKS5 UDP 域名截断")
		}
		host = string(pkt[off : off+dlen])
		off += dlen
	case 0x04:
		if len(pkt) < off+16+2 {
			return "", 0, nil, errors.New("SOCKS5 UDP IPv6 地址截断")
		}
		host = net.IP(pkt[off : off+16]).String()
		off += 16
	default:
		return "", 0, nil, fmt.Errorf("不支持的 SOCKS5 UDP 地址类型: 0x%02x", pkt[3])
	}
	port = int(binary.BigEndian.Uint16(pkt[off : off+2]))
	off += 2
	return host, port, pkt[off:], nil
}
